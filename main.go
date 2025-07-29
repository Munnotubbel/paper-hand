package main

import (
	"context"
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
		ratedDB.Migrator().DropTable(&models.RatedPaper{})
	}
	logging.Info("Running database auto-migration...")
	rawDB.AutoMigrate(&models.Paper{}, &models.Substance{}, &models.SearchFilter{})
	ratedDB.AutoMigrate(&models.RatedPaper{})

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
	setupPaperRoutes(router, rawDB)
	setupSubstanceRoutes(router, rawDB)
	setupSearchFilterRoutes(router, rawDB)
	setupSearchRoutes(router, fetchService)
	setupRatedPaperRoutes(router, ratedDB)

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

func setupPaperRoutes(router *gin.Engine, db *gorm.DB) {
	rg := router.Group("/papers")

	rg.GET("/", func(c *gin.Context) {
		var papers []models.Paper
		query := db.Model(&models.Paper{})

		// Filter für transfer_n8n
		if transferN8N, ok := c.GetQuery("transfer_n8n"); ok {
			b, err := strconv.ParseBool(transferN8N)
			if err == nil {
				query = query.Where("transfer_n8n = ?", b)
			}
		}

		// Filter für substance
		if substance, ok := c.GetQuery("substance"); ok && substance != "" {
			query = query.Where("substance = ?", substance)
		}

		// Filter für study_design
		if studyDesign, ok := c.GetQuery("study_design"); ok && studyDesign != "" {
			query = query.Where("study_design = ?", studyDesign)
		}

		// Limit für die Anzahl der Ergebnisse
		if limit, ok := c.GetQuery("limit"); ok {
			l, err := strconv.Atoi(limit)
			if err == nil && l > 0 {
				query = query.Limit(l)
			}
		}

		if err := query.Order("created_at desc").Find(&papers).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}

		c.JSON(http.StatusOK, papers)
	})

	rg.PUT("/:id", func(c *gin.Context) {
		id := c.Param("id")
		var paper models.Paper
		if err := db.First(&paper, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "paper not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		// Nur die Felder aktualisieren, die im Request Body gesendet werden
		if err := c.ShouldBindJSON(&paper); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
		db.Model(&models.Paper{}).Where("id = ?", id).Updates(paper)
		c.JSON(http.StatusOK, paper)
	})
}

func setupSubstanceRoutes(router *gin.Engine, db *gorm.DB) {
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

func setupSearchFilterRoutes(router *gin.Engine, db *gorm.DB) {
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

func setupRatedPaperRoutes(router *gin.Engine, db *gorm.DB) {
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

		err := db.Clauses(clause.OnConflict{
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
		if err := db.Where("doi = ?", doi).First(&ratedPaper).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "Rated paper not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			return
		}
		c.JSON(http.StatusOK, ratedPaper)
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
