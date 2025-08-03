package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"paper-hand/config"
	"paper-hand/models"
	"paper-hand/providers"
	"paper-hand/providers/europepmc"
	"paper-hand/providers/pubmed"
	"paper-hand/providers/unpaywall"
	"paper-hand/services"
	"paper-hand/storage"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var newPapersCounter prometheus.Counter

func init() {
	newPapersCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "new_papers_added_total",
			Help: "Total number of new papers added to the database.",
		},
	)
	prometheus.MustRegister(newPapersCounter)
}

func apiKeyAuthMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg.APISecretKey == "" {
			c.Next()
			return
		}
		apiKey := c.GetHeader("X-API-KEY")
		if apiKey != cfg.APISecretKey {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: Invalid API Key"})
			return
		}
		c.Next()
	}
}

func main() {
	logging, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer logging.Sync()

	cfg, err := config.Load()
	if err != nil {
		logging.Fatal("Config load error", zap.Error(err))
	}

	// Setup Database Connections
	rawDB, err := gorm.Open(postgres.Open(cfg.RawDSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		logging.Fatal("Failed to connect to raw database", zap.Error(err))
	}
	logging.Info("Successfully connected to raw papers database.")

	ratedDB, err := gorm.Open(postgres.Open(cfg.RatedDSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		logging.Fatal("Failed to connect to rated database", zap.Error(err))
	}
	logging.Info("Successfully connected to rated papers database.")

	// Auto-Migration
	if gin.Mode() == gin.DebugMode {
		logging.Info("Debug mode detected. Dropping tables for fresh start.")
		rawDB.Migrator().DropTable(&models.Paper{}, &models.Substance{}, &models.SearchFilter{})
		ratedDB.Migrator().DropTable(&models.RatedPaper{}, &models.ContentArticle{})
	}
	logging.Info("Running database auto-migration...")
	rawDB.AutoMigrate(&models.Paper{}, &models.Substance{}, &models.SearchFilter{})
	ratedDB.AutoMigrate(&models.RatedPaper{}, &models.ContentArticle{})

	// Seeding
	seedDefaultSubstances(rawDB, logging)
	seedDefaultSearchFilters(rawDB, logging)

	// Setup Providers
	enabledProviderNames := strings.Split(cfg.EnabledProviders, ",")
	var enabledProviders []providers.Provider
	for _, name := range enabledProviderNames {
		switch name {
		case "pubmed":
			enabledProviders = append(enabledProviders, pubmed.NewFetcher(cfg, logging))
		case "europepmc":
			enabledProviders = append(enabledProviders, europepmc.NewFetcher(cfg, logging))
		default:
			logging.Warn("Unknown provider in config", zap.String("provider_name", name))
		}
	}
	if len(enabledProviders) == 0 {
		logging.Fatal("No valid providers enabled. Check ENABLED_PROVIDERS in .env")
	}
	logging.Info("Active providers loaded", zap.Strings("providers", enabledProviderNames))

	// Setup Services
	s3Client, err := storage.NewS3Client(cfg)
	if err != nil {
		logging.Fatal("S3 client creation failed", zap.Error(err))
	}
	unpaywallFetcher := unpaywall.NewFetcher(cfg, logging)
	fetchService := services.NewFetchService(cfg, rawDB, s3Client, logging, enabledProviders, unpaywallFetcher)

	// Setup Router
	router := gin.Default()
	router.Use(gin.Recovery())
	router.Use(apiKeyAuthMiddleware(cfg))
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Setup Routes
	setupPaperRoutes(router, rawDB, logging)
	setupSubstanceRoutes(router, rawDB, logging)
	setupSearchFilterRoutes(router, rawDB, logging)
	setupSearchRoutes(router, fetchService)
	setupRatedPaperRoutes(router, ratedDB, rawDB, logging)
	setupContentArticleRoutes(router, ratedDB, logging)
	setupCitationRoutes(router, logging)

	// Setup Cron
	cronScheduler := cron.New()
	cronScheduler.AddFunc(cfg.CronSchedule, func() {
		logging.Info("Running scheduled fetch job...")
		count, err := fetchService.RunAllSubstances(context.Background())
		if err != nil {
			logging.Error("Cron job failed", zap.Error(err))
		} else {
			logging.Info("Cron job completed", zap.Int("new_papers", count))
			newPapersCounter.Add(float64(count))
		}
	})
	cronScheduler.Start()

	logging.Info("Starting server", zap.String("port", cfg.HTTPPort))
	if err := router.Run(":" + cfg.HTTPPort); err != nil {
		logging.Fatal("Failed to run server", zap.Error(err))
	}
}

func setupPaperRoutes(router *gin.Engine, db *gorm.DB, log *zap.Logger) {
	rg := router.Group("/papers")

	// Einfacher GET-Endpunkt, um alle Paper abzurufen (ohne Filter)
	rg.GET("/", func(c *gin.Context) {
		var papers []models.Paper
		if err := db.Find(&papers).Error; err != nil {
			log.Error("Database query for all papers failed", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		c.JSON(http.StatusOK, papers)
	})

	// Neuer, body-gesteuerter Endpunkt für komplexe Abfragen
	rg.POST("/query", func(c *gin.Context) {
		type PaperQuery struct {
			Substance   string `json:"substance"`
			TransferN8N *bool  `json:"transfer_n8n"`
			CloudStored *bool  `json:"cloud_stored"`
			NoPDFFound  *bool  `json:"no_pdf_found"`
			Limit       int    `json:"limit"`
		}

		var req PaperQuery
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			return
		}

		query := db.Model(&models.Paper{})

		if req.Substance != "" {
			query = query.Where("substance = ?", req.Substance)
		}
		if req.TransferN8N != nil {
			query = query.Where("transfer_n8n = ?", *req.TransferN8N)
		}
		if req.CloudStored != nil {
			query = query.Where("cloud_stored = ?", *req.CloudStored)
		}
		if req.NoPDFFound != nil {
			query = query.Where("no_pdf_found = ?", *req.NoPDFFound)
		}
		if req.Limit > 0 {
			query = query.Limit(req.Limit)
		}

		var papers []models.Paper
		if err := query.Order("created_at desc").Find(&papers).Error; err != nil {
			log.Error("Database query for papers failed", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}

		c.JSON(http.StatusOK, papers)
	})

	// PUT-Endpunkt zum Aktualisieren bleibt gleich
	rg.PUT("/:id", func(c *gin.Context) {
		id := c.Param("id")

		// Zuerst prüfen, ob das Paper existiert
		var paper models.Paper
		if err := db.First(&paper, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "paper not found"})
				return
			}
			log.Error("DB error checking for paper on PUT", zap.String("id", id), zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}

		// Nur die gesendeten Felder binden, um Überschreiben zu verhindern
		var updateData map[string]interface{}
		if err := c.ShouldBindJSON(&updateData); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		// Update ausführen
		if err := db.Model(&paper).Updates(updateData).Error; err != nil {
			log.Error("DB error updating paper", zap.String("id", id), zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update paper"})
			return
		}

		c.JSON(http.StatusOK, paper)
	})
}

func setupSubstanceRoutes(router *gin.Engine, db *gorm.DB, log *zap.Logger) {
	rg := router.Group("/substances")
	rg.POST("/", func(c *gin.Context) {
		var sub models.Substance
		if err := c.ShouldBindJSON(&sub); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
		if err := db.Create(&sub).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create substance"})
			return
		}
		c.JSON(http.StatusCreated, sub)
	})
	rg.GET("/", func(c *gin.Context) {
		var subs []models.Substance
		if err := db.Find(&subs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		c.JSON(http.StatusOK, subs)
	})
}

func setupSearchFilterRoutes(router *gin.Engine, db *gorm.DB, log *zap.Logger) {
	rg := router.Group("/search-filters")
	rg.POST("/", func(c *gin.Context) {
		var filter models.SearchFilter
		if err := c.ShouldBindJSON(&filter); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
		if err := db.Create(&filter).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create filter"})
			return
		}
		c.JSON(http.StatusCreated, filter)
	})
	rg.GET("/", func(c *gin.Context) {
		var filters []models.SearchFilter
		if err := db.Find(&filters).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		c.JSON(http.StatusOK, filters)
	})
}

func setupSearchRoutes(router *gin.Engine, fetchService *services.FetchService) {
	rg := router.Group("/search")
	rg.POST("/all", func(c *gin.Context) {
		go func() {
			count, err := fetchService.RunAllSubstances(context.Background())
			if err != nil {
				fetchService.Logger.Error("Async all-substance fetch failed", zap.Error(err))
			} else {
				newPapersCounter.Add(float64(count))
				fetchService.Logger.Info("Async all-substance fetch completed", zap.Int("total_new_papers", count))
			}
		}()
		c.JSON(http.StatusAccepted, gin.H{"message": "Search for all substances triggered."})
	})
	rg.POST("/substance/:id", func(c *gin.Context) {
		id := c.Param("id")
		var sub models.Substance
		if err := fetchService.DB.First(&sub, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "substance not found"})
			return
		}
		var filters []models.SearchFilter
		fetchService.DB.Find(&filters)

		go func() {
			count, err := fetchService.RunForSubstance(context.Background(), sub, filters)
			if err != nil {
				fetchService.Logger.Error("Async single fetch failed", zap.Error(err))
			} else {
				newPapersCounter.Add(float64(count))
				fetchService.Logger.Info("Async single fetch completed", zap.Int("new_papers", count), zap.String("substance", sub.Name))
			}
		}()
		c.JSON(http.StatusAccepted, gin.H{"message": fmt.Sprintf("Search for substance %s triggered.", sub.Name)})
	})
}

func setupRatedPaperRoutes(router *gin.Engine, ratedDB *gorm.DB, rawDB *gorm.DB, log *zap.Logger) {
	rg := router.Group("/rated-papers")
	rg.POST("/", func(c *gin.Context) {
		var ratedPaper models.RatedPaper
		if err := c.ShouldBindJSON(&ratedPaper); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			return
		}

		// Alle Felder, die bei einem Konflikt aktualisiert werden sollen.
		updateColumns := []string{
			"s3_link", "rating", "confidence_score", "category", "ai_summary",
			"key_findings", "study_strengths", "study_limitations",
			"content_idea", "content_status", "content_url", "processed",
		}

		err := ratedDB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "doi"}},
			DoUpdates: clause.AssignmentColumns(updateColumns),
		}).Create(&ratedPaper).Error

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save rated paper"})
			return
		}
		c.JSON(http.StatusOK, ratedPaper)
	})
	rg.GET("/:doi", func(c *gin.Context) {
		doi := c.Param("doi")
		var ratedPaper models.RatedPaper
		if err := ratedDB.Where("doi = ?", doi).First(&ratedPaper).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "Rated paper not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			return
		}

		// PMID und Substance aus rawDB holen
		type RatedPaperWithPMID struct {
			models.RatedPaper
			PMID      string `json:"pmid"`
			Substance string `json:"substance"`
		}

		enrichedPaper := RatedPaperWithPMID{
			RatedPaper: ratedPaper,
			PMID:       "", // Default fallback
			Substance:  "", // Default fallback
		}

		// PMID und Substance aus papers Tabelle holen (über DOI)
		if ratedPaper.DOI != "" {
			var paper models.Paper
			if err := rawDB.Where("doi = ?", ratedPaper.DOI).First(&paper).Error; err == nil {
				enrichedPaper.PMID = paper.PMID
				enrichedPaper.Substance = paper.Substance
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				log.Warn("Failed to fetch PMID and substance for rated paper",
					zap.String("doi", ratedPaper.DOI),
					zap.Error(err))
			}
		}

		c.JSON(http.StatusOK, enrichedPaper)
	})

	// POST - Query rated papers with filters
	rg.POST("/query", func(c *gin.Context) {
		type RatedPaperQuery struct {
			DOI              string   `json:"doi"`
			MinRating        *float64 `json:"min_rating"`        // Rating >= MinRating
			CategoryKeywords []string `json:"category_keywords"` // OR-Suche in Category-Feld
			ContentStatus    string   `json:"content_status"`
			Processed        *bool    `json:"processed"`
			Limit            int      `json:"limit"`
		}

		var req RatedPaperQuery
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			return
		}

		query := ratedDB.Model(&models.RatedPaper{})

		if req.DOI != "" {
			query = query.Where("doi = ?", req.DOI)
		}
		if req.MinRating != nil {
			query = query.Where("rating >= ?", *req.MinRating)
		}
		if len(req.CategoryKeywords) > 0 {
			// OR-Suche für Category-Keywords mit ILIKE (case-insensitive)
			var conditions []string
			var args []interface{}
			for _, keyword := range req.CategoryKeywords {
				conditions = append(conditions, "category ILIKE ?")
				args = append(args, "%"+keyword+"%")
			}
			query = query.Where(strings.Join(conditions, " OR "), args...)
		}
		if req.ContentStatus != "" {
			query = query.Where("content_status = ?", req.ContentStatus)
		}
		if req.Processed != nil {
			query = query.Where("processed = ?", *req.Processed)
		}
		if req.Limit > 0 {
			query = query.Limit(req.Limit)
		}

		var ratedPapers []models.RatedPaper
		if err := query.Order("rating desc, created_at desc").Find(&ratedPapers).Error; err != nil {
			log.Error("Database query for rated papers failed", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}

		// Erweiterte Response-Struktur mit PMID und Substance
		type RatedPaperWithPMID struct {
			models.RatedPaper
			PMID      string `json:"pmid"`
			Substance string `json:"substance"`
		}

		// PMID und Substance für jedes rated paper aus rawDB holen
		var enrichedPapers []RatedPaperWithPMID
		for _, ratedPaper := range ratedPapers {
			enrichedPaper := RatedPaperWithPMID{
				RatedPaper: ratedPaper,
				PMID:       "", // Default fallback
				Substance:  "", // Default fallback
			}

			// PMID und Substance aus papers Tabelle holen (über DOI)
			if ratedPaper.DOI != "" {
				var paper models.Paper
				if err := rawDB.Where("doi = ?", ratedPaper.DOI).First(&paper).Error; err == nil {
					enrichedPaper.PMID = paper.PMID
					enrichedPaper.Substance = paper.Substance
				} else if !errors.Is(err, gorm.ErrRecordNotFound) {
					// Nur loggen bei echten DB-Fehlern, nicht bei "not found"
					log.Warn("Failed to fetch PMID and substance for rated paper",
						zap.String("doi", ratedPaper.DOI),
						zap.Error(err))
				}
			}

			enrichedPapers = append(enrichedPapers, enrichedPaper)
		}

		c.JSON(http.StatusOK, enrichedPapers)
	})
}

func setupContentArticleRoutes(router *gin.Engine, db *gorm.DB, log *zap.Logger) {
	rg := router.Group("/content-articles")

	// POST - Create new content article
	rg.POST("/", func(c *gin.Context) {
		var article models.ContentArticle
		if err := c.ShouldBindJSON(&article); err != nil {
			log.Error("Invalid request body for content article creation", zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			return
		}

		if err := db.Create(&article).Error; err != nil {
			log.Error("Failed to create content article", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save content article"})
			return
		}

		log.Info("Content article created successfully", zap.Uint("id", article.ID), zap.String("title", article.Title))
		c.JSON(http.StatusCreated, article)
	})

	// PUT - Update content article by ID
	rg.PUT("/:id", func(c *gin.Context) {
		id := c.Param("id")
		var article models.ContentArticle

		// Check if article exists
		if err := db.First(&article, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "Content article not found"})
				return
			}
			log.Error("Database error while fetching content article", zap.String("id", id), zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			return
		}

		// Bind new data
		if err := c.ShouldBindJSON(&article); err != nil {
			log.Error("Invalid request body for content article update", zap.String("id", id), zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			return
		}

		// Save updates
		if err := db.Save(&article).Error; err != nil {
			log.Error("Failed to update content article", zap.String("id", id), zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update content article"})
			return
		}

		log.Info("Content article updated successfully", zap.String("id", id), zap.String("title", article.Title))
		c.JSON(http.StatusOK, article)
	})

	// GET - Get content article by ID
	rg.GET("/:id", func(c *gin.Context) {
		id := c.Param("id")
		var article models.ContentArticle

		if err := db.First(&article, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "Content article not found"})
				return
			}
			log.Error("Database error while fetching content article", zap.String("id", id), zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			return
		}

		c.JSON(http.StatusOK, article)
	})

	// POST - Query content articles with filters
	rg.POST("/query", func(c *gin.Context) {
		type ContentQuery struct {
			Substance     string `json:"substance"`
			PMID          string `json:"pmid"`
			DOI           string `json:"doi"`
			ContentStatus string `json:"content_status"`
			Category      string `json:"category"`
			AuthorName    string `json:"author_name"`
			StudyType     string `json:"study_type"`
			BlogPosted    *bool  `json:"blog_posted"`
			Limit         int    `json:"limit"`
		}

		var req ContentQuery
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			return
		}

		query := db.Model(&models.ContentArticle{})

		if req.Substance != "" {
			query = query.Where("substance = ?", req.Substance)
		}
		if req.PMID != "" {
			query = query.Where("pmid = ?", req.PMID)
		}
		if req.DOI != "" {
			query = query.Where("doi = ?", req.DOI)
		}
		if req.ContentStatus != "" {
			query = query.Where("content_status = ?", req.ContentStatus)
		}
		if req.Category != "" {
			query = query.Where("category = ?", req.Category)
		}
		if req.AuthorName != "" {
			query = query.Where("author_name = ?", req.AuthorName)
		}
		if req.StudyType != "" {
			query = query.Where("study_type = ?", req.StudyType)
		}
		if req.BlogPosted != nil {
			query = query.Where("blog_posted = ?", *req.BlogPosted)
		}
		if req.Limit > 0 {
			query = query.Limit(req.Limit)
		}

		var articles []models.ContentArticle
		if err := query.Order("created_at desc").Find(&articles).Error; err != nil {
			log.Error("Database query for content articles failed", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}

		c.JSON(http.StatusOK, articles)
	})
}

func seedDefaultSubstances(db *gorm.DB, logger *zap.Logger) {
	var count int64
	db.Model(&models.Substance{}).Count(&count)
	if count > 0 {
		return
	}
	substances := []models.Substance{
		{Name: "curcumin"},
		{Name: "bisdemethoxycurcumin"},
		{Name: "demethoxycurcumin"},
	}
	if err := db.Create(&substances).Error; err != nil {
		logger.Warn("Failed to seed default substances", zap.Error(err))
	} else {
		logger.Info("Default substances seeded.")
	}
}

func seedDefaultSearchFilters(db *gorm.DB, logger *zap.Logger) {
	var count int64
	db.Model(&models.SearchFilter{}).Count(&count)
	if count > 0 {
		return
	}
	filters := []models.SearchFilter{
		{Name: "Meta-Analysis (Human)", FilterQuery: `("meta-analysis"[Publication Type] OR "systematic review"[Publication Type]) AND "humans"[MeSH Terms]`},
		{Name: "RCT (Human)", FilterQuery: `"randomized controlled trial"[Publication Type] AND "humans"[MeSH Terms]`},
		{Name: "Review (Human)", FilterQuery: `"review"[Publication Type] AND "humans"[MeSH Terms]`},
	}
	if err := db.Create(&filters).Error; err != nil {
		logger.Warn("Failed to seed default search filters", zap.Error(err))
	} else {
		logger.Info("Default search filters seeded.")
	}
}

// setupCitationRoutes konfiguriert alle Citation-bezogenen API-Routen
func setupCitationRoutes(router *gin.Engine, log *zap.Logger) {
	citationExtractor := services.NewCitationExtractor(log)
	rg := router.Group("/citations")

	// POST - Extract citations and references from text
	rg.POST("/extract", func(c *gin.Context) {
		var request struct {
			Text string `json:"text" binding:"required"`
		}

		if err := c.ShouldBindJSON(&request); err != nil {
			log.Error("Invalid request body for citation extraction", zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body. 'text' field is required."})
			return
		}

		if len(request.Text) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Text cannot be empty"})
			return
		}

		log.Info("Starting citation extraction",
			zap.Int("text_length", len(request.Text)))

		result, err := citationExtractor.ExtractCitations(c.Request.Context(), request.Text)
		if err != nil {
			log.Error("Failed to extract citations", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to extract citations"})
			return
		}

		log.Info("Citation extraction completed successfully",
			zap.Int("in_text_citations", result.CitationCount),
			zap.Int("full_references", result.ReferenceCount))

		c.JSON(http.StatusOK, result)
	})

	// POST - Extract citations for n8n workflow (returns formatted text)
	rg.POST("/extract-for-n8n", func(c *gin.Context) {
		var request struct {
			Text string `json:"text" binding:"required"`
		}

		if err := c.ShouldBindJSON(&request); err != nil {
			log.Error("Invalid request body for n8n citation extraction", zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body. 'text' field is required."})
			return
		}

		if len(request.Text) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Text cannot be empty"})
			return
		}

		log.Info("Starting n8n citation extraction",
			zap.Int("text_length", len(request.Text)))

		result, err := citationExtractor.ExtractCitations(c.Request.Context(), request.Text)
		if err != nil {
			log.Error("Failed to extract citations for n8n", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to extract citations"})
			return
		}

		// Format für n8n Workflow
		formattedOutput := citationExtractor.FormatForN8N(result)

		log.Info("N8N citation extraction completed successfully",
			zap.Int("in_text_citations", result.CitationCount),
			zap.Int("full_references", result.ReferenceCount))

		// n8n erwartet oft JSON mit einem "output" Feld
		c.JSON(http.StatusOK, gin.H{
			"output": formattedOutput,
			"statistics": gin.H{
				"in_text_citations": result.CitationCount,
				"full_references":   result.ReferenceCount,
			},
		})
	})

	// POST - Inject citations into simplified text
	rg.POST("/inject", func(c *gin.Context) {
		var request struct {
			SimplifiedText   string                     `json:"simplified_text" binding:"required"`
			OriginalMappings []services.CitationMapping `json:"original_mappings" binding:"required"`
		}

		if err := c.ShouldBindJSON(&request); err != nil {
			log.Error("Invalid request body for citation injection", zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body. 'simplified_text' and 'original_mappings' fields are required."})
			return
		}

		if len(request.SimplifiedText) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Simplified text cannot be empty"})
			return
		}

		log.Info("Starting citation injection",
			zap.Int("text_length", len(request.SimplifiedText)),
			zap.Int("mappings_count", len(request.OriginalMappings)))

		result, err := citationExtractor.InjectCitations(c.Request.Context(), request.SimplifiedText, request.OriginalMappings)
		if err != nil {
			log.Error("Failed to inject citations", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to inject citations"})
			return
		}

		log.Info("Citation injection completed successfully")

		c.JSON(http.StatusOK, gin.H{
			"enhanced_text":   result,
			"original_length": len(request.SimplifiedText),
			"enhanced_length": len(result),
		})
	})

	// POST - Inject citations for n8n workflow (simplified interface)
	rg.POST("/inject-for-n8n", func(c *gin.Context) {
		var request struct {
			SimplifiedText string `json:"simplified_text" binding:"required"`
			MappingsJSON   string `json:"mappings_json" binding:"required"`
		}

		if err := c.ShouldBindJSON(&request); err != nil {
			log.Error("Invalid request body for n8n citation injection", zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			return
		}

		// Parse mappings from JSON string
		var mappings []services.CitationMapping
		if err := json.Unmarshal([]byte(request.MappingsJSON), &mappings); err != nil {
			log.Error("Failed to parse mappings JSON", zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid mappings JSON format"})
			return
		}

		log.Info("Starting n8n citation injection",
			zap.Int("text_length", len(request.SimplifiedText)),
			zap.Int("mappings_count", len(mappings)))

		result, err := citationExtractor.InjectCitations(c.Request.Context(), request.SimplifiedText, mappings)
		if err != nil {
			log.Error("Failed to inject citations for n8n", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to inject citations"})
			return
		}

		log.Info("N8N citation injection completed successfully")

		// n8n-friendly response format
		c.JSON(http.StatusOK, gin.H{
			"output":  result,
			"success": true,
			"statistics": gin.H{
				"original_length": len(request.SimplifiedText),
				"enhanced_length": len(result),
				"mappings_used":   len(mappings),
			},
		})
	})

	// GET - Health check for citation service
	rg.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":   "healthy",
			"service":  "citation-extractor",
			"version":  "2.0.0", // Updated version with mapping support
			"features": []string{"extract", "inject", "mappings", "n8n-integration"},
		})
	})

	log.Info("Citation routes configured successfully",
		zap.String("base_path", "/citations"),
		zap.Strings("endpoints", []string{"/extract", "/extract-for-n8n", "/inject", "/inject-for-n8n", "/health"}))
}
