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
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
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
	rawDB.AutoMigrate(&models.Paper{}, &models.Substance{}, &models.SearchFilter{}, &models.PaperLink{})
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
	setupTextRoutes(router, logging)
	setupGraphRoutes(router, rawDB, logging)
	setupAnswerRoutes(router, logging)

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
	srv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           router,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
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

// setupGraphRoutes konfiguriert Paper-Graph-Endpoints
func setupGraphRoutes(router *gin.Engine, rawDB *gorm.DB, log *zap.Logger) {
	rg := router.Group("/graph/paper-links")

	// DOI/PMID Normalisierung (einfach; optional: robustere Varianten)
	doiNorm := func(s string) string {
		s = strings.TrimSpace(strings.ToLower(s))
		// Entferne URL-Präfixe
		s = strings.TrimPrefix(s, "https://doi.org/")
		s = strings.TrimPrefix(s, "http://doi.org/")
		s = strings.TrimPrefix(s, "doi:")
		return strings.TrimSpace(s)
	}
	pmidNorm := func(s string) string {
		s = strings.TrimSpace(s)
		// nur Ziffern extrahieren
		var out strings.Builder
		for _, r := range s {
			if r >= '0' && r <= '9' {
				out.WriteRune(r)
			}
		}
		return out.String()
	}

	type LinkInput struct {
		Source struct {
			DOI  string `json:"doi"`
			PMID string `json:"pmid"`
		} `json:"source"`
		Citations []struct {
			DOI         string         `json:"doi"`
			PMID        string         `json:"pmid"`
			Evidence    map[string]any `json:"evidence"`
			TargetTable string         `json:"target_table"`
		} `json:"citations"`
		SourceTable string `json:"source_table"`
	}

	// POST - Upsert links
	rg.POST("/upsert", func(c *gin.Context) {
		var req LinkInput
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			return
		}
		srcDOI, srcPMID := doiNorm(req.Source.DOI), pmidNorm(req.Source.PMID)
		if srcDOI == "" && srcPMID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "source doi or pmid required"})
			return
		}
		type upResult struct {
			Inserted int
			Updated  int
		}
		res := upResult{}
		for _, cit := range req.Citations {
			tgtDOI, tgtPMID := doiNorm(cit.DOI), pmidNorm(cit.PMID)
			if tgtDOI == "" && tgtPMID == "" {
				continue
			}

			link := models.PaperLink{
				SourceDOINorm: srcDOI, SourcePMIDNorm: srcPMID,
				TargetDOINorm: tgtDOI, TargetPMIDNorm: tgtPMID,
				SourceDOI: req.Source.DOI, SourcePMID: req.Source.PMID,
				TargetDOI: cit.DOI, TargetPMID: cit.PMID,
				SourceTable: req.SourceTable, TargetTable: cit.TargetTable,
			}
			// Evidence mergen (bestehende JSON ergänzen)
			if len(cit.Evidence) > 0 {
				b, _ := json.Marshal(cit.Evidence)
				link.Evidence = b
			}
			// Upsert auf Unique-Edge
			if err := rawDB.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "source_doi_norm"}, {Name: "source_pmid_norm"}, {Name: "target_doi_norm"}, {Name: "target_pmid_norm"}},
				DoUpdates: clause.Assignments(map[string]any{
					"source_doi":   link.SourceDOI,
					"source_pmid":  link.SourcePMID,
					"target_doi":   link.TargetDOI,
					"target_pmid":  link.TargetPMID,
					"source_table": link.SourceTable,
					"target_table": link.TargetTable,
					"evidence":     link.Evidence,
					"updated_at":   gorm.Expr("NOW()"),
				}),
			}).Create(&link).Error; err != nil {
				log.Error("Failed to upsert paper link", zap.Error(err))
				c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
				return
			}
			// GORM liefert RowsAffected in db-Objekt, hier approximieren wir Insert/Update nicht fein-granular
			res.Updated++
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "updated": res.Updated, "inserted": res.Inserted})
	})

	// GET by DOI
	rg.GET("/by-doi/:doi", func(c *gin.Context) {
		doi := doiNorm(c.Param("doi"))
		if doi == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid doi"})
			return
		}
		var links []models.PaperLink
		if err := rawDB.Where("source_doi_norm = ? OR target_doi_norm = ?", doi, doi).Find(&links).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		c.JSON(http.StatusOK, links)
	})
	// GET by PMID
	rg.GET("/by-pmid/:pmid", func(c *gin.Context) {
		pmid := pmidNorm(c.Param("pmid"))
		if pmid == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid pmid"})
			return
		}
		var links []models.PaperLink
		if err := rawDB.Where("source_pmid_norm = ? OR target_pmid_norm = ?", pmid, pmid).Find(&links).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		c.JSON(http.StatusOK, links)
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
			"content_idea", "content_status", "content_url", "processed", "added_rag",
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
	// NEU: General Update Endpoint
	rg.PATCH("/", func(c *gin.Context) {
		// Payload mit allen optionalen Feldern
		var payload struct {
			DOI           string  `json:"doi" binding:"required"`
			ContentStatus *string `json:"content_status"`
			ContentURL    *string `json:"content_url"`
			Processed     *bool   `json:"processed"`
			AddedRag      *bool   `json:"added_rag"`
			Outline       string  `json:"outline"`
			Citations     string  `json:"citations"`
			DeepResearch  string  `json:"deep_research"`
		}
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Missing or invalid fields (doi required)"})
			return
		}

		// Map nur mit den mitgesendeten Feldern befüllen
		updates := map[string]interface{}{}
		if payload.ContentStatus != nil {
			updates["content_status"] = *payload.ContentStatus
		}
		if payload.ContentURL != nil {
			updates["content_url"] = *payload.ContentURL
		}
		if payload.Processed != nil {
			updates["processed"] = *payload.Processed
		}
		if payload.AddedRag != nil {
			updates["added_rag"] = *payload.AddedRag
		}

		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No updatable fields provided"})
			return
		}

		if err := ratedDB.
			Model(&models.RatedPaper{}).
			Where("doi = ?", payload.DOI).
			Updates(updates).
			Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "Rated paper not found"})
			} else {
				log.Error("Failed to update rated paper", zap.Error(err))
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			}
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "updated fields",
			"updates": updates,
		})
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

	rg.PATCH("/added-rag", func(c *gin.Context) {
		var req struct {
			DOI            string `json:"doi" binding:"required"`
			AddedRag       *bool  `json:"added_rag"`
			Processed      *bool  `json:"processed"`
			LightRAGDocID  string `json:"lightrag_doc_id"`
			ReferencesJSON string `json:"references_json"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid body: doi required"})
			return
		}
		if req.DOI == "" || !strings.Contains(req.DOI, "/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid DOI format"})
			return
		}
		updates := map[string]any{}
		if req.AddedRag != nil {
			updates["added_rag"] = *req.AddedRag
		}
		if req.Processed != nil {
			updates["processed"] = *req.Processed
		}
		if req.LightRAGDocID != "" {
			updates["lightrag_doc_id"] = req.LightRAGDocID
		}
		if req.ReferencesJSON != "" {
			updates["references_json"] = req.ReferencesJSON
		}
		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No updatable fields provided"})
			return
		}
		if err := ratedDB.Model(&models.RatedPaper{}).
			Where("doi = ?", req.DOI).
			Updates(updates).Error; err != nil {
			log.Error("Failed to update rated paper (added-rag)", zap.String("doi", req.DOI), zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "updated", "updates": updates})
	})

	// POST - Query rated papers with filters
	rg.POST("/query", func(c *gin.Context) {
		type RatedPaperQuery struct {
			DOI              string   `json:"doi"`
			MinRating        *float64 `json:"min_rating"`        // Rating >= MinRating
			CategoryKeywords []string `json:"category_keywords"` // OR-Suche in Category-Feld
			ContentStatus    string   `json:"content_status"`
			Processed        *bool    `json:"processed"`
			AddedRag         *bool    `json:"added_rag"`
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
			if *req.Processed {
				query = query.Where("processed = ?", true)
			} else {
				query = query.Where("processed = ? OR processed IS NULL)", false)
			}

		}
		if req.AddedRag != nil {
			if *req.AddedRag {
				// nur TRUE zulassen
				query = query.Where("added_rag = ?", true)
			} else {
				// FALSE oder NULL zulassen
				query = query.Where("(added_rag = ? OR added_rag IS NULL)", false)
			}
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

// setupTextRoutes konfiguriert Text-bezogene API-Routen (z. B. Normalisierung)
func setupTextRoutes(router *gin.Engine, log *zap.Logger) {
	normalizer := services.NewTextNormalizer(log)
	rg := router.Group("/text")

	// POST - Normalize heterogeneous PDF extract into unified full_text
	rg.POST("/normalize-for-n8n", func(c *gin.Context) {
		// Body generisch lesen, um n8n-String-Optionen ("true") robust zu akzeptieren
		raw := map[string]any{}
		if err := c.ShouldBindBodyWith(&raw, binding.JSON); err != nil {
			log.Error("Invalid request body for text normalization", zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body. 'pdf_extract' field is required."})
			return
		}

		// Helper: Coercion
		coerceBool := func(v any) (bool, bool) {
			switch t := v.(type) {
			case bool:
				return t, true
			case string:
				s := strings.TrimSpace(strings.ToLower(t))
				if s == "true" || s == "1" || s == "yes" || s == "on" {
					return true, true
				}
				if s == "false" || s == "0" || s == "no" || s == "off" {
					return false, true
				}
				return false, false
			case float64:
				return t != 0, true
			case int:
				return t != 0, true
			default:
				return false, false
			}
		}
		coerceFloat := func(v any) (float64, bool) {
			switch t := v.(type) {
			case float64:
				return t, true
			case string:
				f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
				if err == nil {
					return f, true
				}
				return 0, false
			case int:
				return float64(t), true
			default:
				return 0, false
			}
		}
		coerceInt := func(v any) (int, bool) {
			switch t := v.(type) {
			case float64:
				return int(t), true
			case int:
				return t, true
			case string:
				i, err := strconv.Atoi(strings.TrimSpace(t))
				if err == nil {
					return i, true
				}
				return 0, false
			default:
				return 0, false
			}
		}

		// pdf_extract holen (robust: akzeptiere auch pdf_extract_json (string) oder pdf_text (string))
		pdfExtract, ok := raw["pdf_extract"]
		if !ok || pdfExtract == nil {
			// Versuch: pdf_extract_json als String mit JSON
			if v, ok2 := raw["pdf_extract_json"].(string); ok2 && strings.TrimSpace(v) != "" {
				var tmp any
				if err := json.Unmarshal([]byte(v), &tmp); err == nil {
					pdfExtract = tmp
				}
			}
		}
		if pdfExtract == nil {
			// letzter Fallback: pdf_text (nur Text)
			if v, ok2 := raw["pdf_text"].(string); ok2 && strings.TrimSpace(v) != "" {
				pdfExtract = v
			}
		}
		if pdfExtract == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body. 'pdf_extract' (or 'pdf_extract_json'/'pdf_text') field is required."})
			return
		}

		// Default options
		opts := services.NormalizeOptions{
			NormalizeUnicode:      true,
			FixHyphenation:        true,
			CollapseWhitespace:    true,
			HeaderFooterDetection: true,
			HeaderFooterThreshold: 0.6,
			MinArtifactLineLen:    0,
			KeepPageBreaks:        false,
			LanguageHint:          "",
			// advanced defaults
			StripPublisherBoilerplate: false,
			StripFiguresAndTables:     true,
			StripFrontMatter:          false,
			StripCorrespondenceEmails: true,
			PublisherHint:             "",
		}
		// Override: Top-Level Keys
		if v, ok := raw["normalize_unicode"]; ok {
			if b, ok2 := coerceBool(v); ok2 {
				opts.NormalizeUnicode = b
			}
		}
		if v, ok := raw["fix_hyphenation"]; ok {
			if b, ok2 := coerceBool(v); ok2 {
				opts.FixHyphenation = b
			}
		}
		if v, ok := raw["collapse_whitespace"]; ok {
			if b, ok2 := coerceBool(v); ok2 {
				opts.CollapseWhitespace = b
			}
		}
		if v, ok := raw["header_footer_detection"]; ok {
			if b, ok2 := coerceBool(v); ok2 {
				opts.HeaderFooterDetection = b
			}
		}
		if v, ok := raw["header_footer_threshold"]; ok {
			if f, ok2 := coerceFloat(v); ok2 {
				opts.HeaderFooterThreshold = f
			}
		}
		if v, ok := raw["min_artifact_line_len"]; ok {
			if i, ok2 := coerceInt(v); ok2 {
				opts.MinArtifactLineLen = i
			}
		}
		if v, ok := raw["keep_page_breaks"]; ok {
			if b, ok2 := coerceBool(v); ok2 {
				opts.KeepPageBreaks = b
			}
		}
		if v, ok := raw["language_hint"]; ok {
			if s, ok2 := v.(string); ok2 {
				opts.LanguageHint = s
			}
		}
		if v, ok := raw["strip_publisher_boilerplate"]; ok {
			if b, ok2 := coerceBool(v); ok2 {
				opts.StripPublisherBoilerplate = b
			}
		}
		if v, ok := raw["strip_figures_and_tables"]; ok {
			if b, ok2 := coerceBool(v); ok2 {
				opts.StripFiguresAndTables = b
			}
		}
		if v, ok := raw["strip_front_matter"]; ok {
			if b, ok2 := coerceBool(v); ok2 {
				opts.StripFrontMatter = b
			}
		}
		if v, ok := raw["strip_correspondence_emails"]; ok {
			if b, ok2 := coerceBool(v); ok2 {
				opts.StripCorrespondenceEmails = b
			}
		}
		if v, ok := raw["publisher_hint"]; ok {
			if s, ok2 := v.(string); ok2 {
				opts.PublisherHint = s
			}
		}

		// Backward-compat: nested options
		if optRaw, ok := raw["options"].(map[string]any); ok {
			if v, ok := optRaw["normalize_unicode"]; ok {
				if b, ok2 := coerceBool(v); ok2 {
					opts.NormalizeUnicode = b
				}
			}
			if v, ok := optRaw["fix_hyphenation"]; ok {
				if b, ok2 := coerceBool(v); ok2 {
					opts.FixHyphenation = b
				}
			}
			if v, ok := optRaw["collapse_whitespace"]; ok {
				if b, ok2 := coerceBool(v); ok2 {
					opts.CollapseWhitespace = b
				}
			}
			if v, ok := optRaw["header_footer_detection"]; ok {
				if b, ok2 := coerceBool(v); ok2 {
					opts.HeaderFooterDetection = b
				}
			}
			if v, ok := optRaw["header_footer_threshold"]; ok {
				if f, ok2 := coerceFloat(v); ok2 {
					opts.HeaderFooterThreshold = f
				}
			}
			if v, ok := optRaw["min_artifact_line_len"]; ok {
				if i, ok2 := coerceInt(v); ok2 {
					opts.MinArtifactLineLen = i
				}
			}
			if v, ok := optRaw["keep_page_breaks"]; ok {
				if b, ok2 := coerceBool(v); ok2 {
					opts.KeepPageBreaks = b
				}
			}
			if v, ok := optRaw["language_hint"]; ok {
				if s, ok2 := v.(string); ok2 {
					opts.LanguageHint = s
				}
			}
			if v, ok := optRaw["strip_publisher_boilerplate"]; ok {
				if b, ok2 := coerceBool(v); ok2 {
					opts.StripPublisherBoilerplate = b
				}
			}
			if v, ok := optRaw["strip_figures_and_tables"]; ok {
				if b, ok2 := coerceBool(v); ok2 {
					opts.StripFiguresAndTables = b
				}
			}
			if v, ok := optRaw["strip_front_matter"]; ok {
				if b, ok2 := coerceBool(v); ok2 {
					opts.StripFrontMatter = b
				}
			}
			if v, ok := optRaw["strip_correspondence_emails"]; ok {
				if b, ok2 := coerceBool(v); ok2 {
					opts.StripCorrespondenceEmails = b
				}
			}
			if v, ok := optRaw["publisher_hint"]; ok {
				if s, ok2 := v.(string); ok2 {
					opts.PublisherHint = s
				}
			}
		}

		log.Info("Starting text normalization for n8n")

		result, err := normalizer.NormalizeExtract(c.Request.Context(), pdfExtract, opts)
		if err != nil {
			if err.Error() == "no text extracted" {
				c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "No extractable text found"})
				return
			}
			log.Error("Failed to normalize extract", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to normalize extract"})
			return
		}

		c.JSON(http.StatusOK, result)
	})

	log.Info("Text routes configured successfully",
		zap.String("base_path", "/text"),
		zap.Strings("endpoints", []string{"/normalize-for-n8n"}),
	)
}

// setupAnswerRoutes provides helper endpoints to ensure numbered citations [n] map to a deterministic bibliography
func setupAnswerRoutes(router *gin.Engine, log *zap.Logger) {
	rg := router.Group("/answers")
	// POST /answers/format-bibliography
	// Body: { answer_text: string, sources: [ {number, doi, pmid, title, year, journal, authors[], doc_id} ] }
	rg.POST("/format-bibliography", func(c *gin.Context) {
		var req struct {
			AnswerText string                `json:"answer_text"`
			Sources    []services.SourceItem `json:"sources"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		ordered, warnings := services.BuildBibliography(req.AnswerText, req.Sources)
		// Render formatted references
		formatted := make([]string, 0, len(ordered))
		for i, s := range ordered {
			// force sequential numbering in output position
			_ = i
			formatted = append(formatted, services.FormatReference(s))
		}
		c.JSON(http.StatusOK, gin.H{
			"ordered_sources": ordered,
			"formatted":       formatted,
			"warnings":        warnings,
		})
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

	// POST - Remove references section (keep in-text citations)
	rg.POST("/remove-references", func(c *gin.Context) {
		var request struct {
			Text string `json:"text" binding:"required"`
		}

		if err := c.ShouldBindJSON(&request); err != nil {
			log.Error("Invalid request body for references removal", zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body. 'text' field is required."})
			return
		}

		if len(request.Text) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Text cannot be empty"})
			return
		}

		log.Info("Starting references section removal",
			zap.Int("text_length", len(request.Text)))

		cleanedText, err := citationExtractor.RemoveReferencesSection(c.Request.Context(), request.Text)
		if err != nil {
			log.Error("Failed to remove references section", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove references section"})
			return
		}

		sizeBefore := len(request.Text)
		sizeAfter := len(cleanedText)
		reductionPercent := int(float64(sizeBefore-sizeAfter) / float64(sizeBefore) * 100)

		log.Info("References section removal completed successfully",
			zap.Int("size_before", sizeBefore),
			zap.Int("size_after", sizeAfter),
			zap.Int("reduction_percent", reductionPercent))

		c.JSON(http.StatusOK, gin.H{
			"cleaned_text": cleanedText,
			"statistics": gin.H{
				"original_size":     sizeBefore,
				"cleaned_size":      sizeAfter,
				"size_reduction":    sizeBefore - sizeAfter,
				"reduction_percent": reductionPercent,
			},
		})
	})

	// POST - Remove references for n8n workflow (simplified response)
	rg.POST("/remove-references-for-n8n", func(c *gin.Context) {
		var request struct {
			Text string `json:"text" binding:"required"`
		}

		if err := c.ShouldBindJSON(&request); err != nil {
			log.Error("Invalid request body for n8n references removal", zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			return
		}

		if len(request.Text) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Text cannot be empty"})
			return
		}

		log.Info("Starting n8n references section removal",
			zap.Int("text_length", len(request.Text)))

		cleanedText, err := citationExtractor.RemoveReferencesSection(c.Request.Context(), request.Text)
		if err != nil {
			log.Error("Failed to remove references section for n8n", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove references section"})
			return
		}

		sizeBefore := len(request.Text)
		sizeAfter := len(cleanedText)
		reductionPercent := int(float64(sizeBefore-sizeAfter) / float64(sizeBefore) * 100)

		log.Info("N8N references section removal completed successfully",
			zap.Int("size_before", sizeBefore),
			zap.Int("size_after", sizeAfter),
			zap.Int("reduction_percent", reductionPercent))

		// n8n-friendly response format
		c.JSON(http.StatusOK, gin.H{
			"output":  cleanedText,
			"success": true,
			"statistics": gin.H{
				"size_reduction_percent": reductionPercent,
				"characters_saved":       sizeBefore - sizeAfter,
			},
		})
	})

	// GET - Health check for citation service
	rg.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":   "healthy",
			"service":  "citation-extractor",
			"version":  "2.1.0", // Updated version with references removal
			"features": []string{"extract", "inject", "mappings", "remove-references", "n8n-integration"},
		})
	})

	log.Info("Citation routes configured successfully",
		zap.String("base_path", "/citations"),
		zap.Strings("endpoints", []string{"/extract", "/extract-for-n8n", "/inject", "/inject-for-n8n", "/remove-references", "/remove-references-for-n8n", "/health"}))
}
