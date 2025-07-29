package main

import (
	"context"
	"fmt"
	"net/http"
	"paper-hand/config"
	"paper-hand/models"
	"paper-hand/providers"
	"paper-hand/providers/europepmc"
	"paper-hand/providers/pubmed"
	"paper-hand/services"
	"paper-hand/storage"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	newPapersCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "new_papers_found_total",
			Help: "Total number of new papers found.",
		},
	)
)

func init() {
	prometheus.MustRegister(newPapersCounter)
}

func apiKeyAuthMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Wenn kein API-Key in der Konfiguration gesetzt ist (z.B. in lokaler Entwicklung),
		// wird die Middleware übersprungen.
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
	cfg, err := config.Load()
	if err != nil {
		panic(fmt.Sprintf("Config load error: %v", err))
	}

	z, _ := zap.NewProduction()
	if gin.IsDebugging() {
		z, _ = zap.NewDevelopment()
	}
	defer z.Sync()
	zap.ReplaceGlobals(z)

	dbLogger := logger.New(
		zap.NewStdLog(z),
		logger.Config{
			SlowThreshold: time.Second,
			LogLevel:      logger.Silent,
			Colorful:      false,
		},
	)
	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{Logger: dbLogger})
	if err != nil {
		z.Fatal("DB connection failed", zap.Error(err))
	}

	if gin.IsDebugging() {
		z.Info("Debug mode: Dropping tables for clean migration.")
		if err := db.Migrator().DropTable(&models.Paper{}, &models.Substance{}, &models.SearchFilter{}); err != nil {
			z.Fatal("Failed to drop tables", zap.Error(err))
		}
	}

	if err := db.AutoMigrate(&models.Paper{}, &models.Substance{}, &models.SearchFilter{}); err != nil {
		z.Fatal("DB migration failed", zap.Error(err))
	}
	seedDefaultSubstances(db, z)
	seedDefaultSearchFilters(db, z)

	enabledProviderNames := strings.Split(cfg.EnabledProviders, ",")
	var enabledProviders []providers.Provider
	for _, name := range enabledProviderNames {
		name = strings.TrimSpace(name)
		switch name {
		case "pubmed":
			enabledProviders = append(enabledProviders, pubmed.NewFetcher(cfg, z))
		case "europepmc":
			enabledProviders = append(enabledProviders, europepmc.NewFetcher(cfg, z))
		default:
			z.Warn("Unknown provider in config", zap.String("provider_name", name))
		}
	}
	if len(enabledProviders) == 0 {
		z.Fatal("No valid providers enabled. Check ENABLED_PROVIDERS in .env")
	}
	z.Info("Active providers loaded", zap.Strings("providers", enabledProviderNames))

	s3Client, err := storage.NewS3Client(cfg)
	if err != nil {
		z.Fatal("S3 client creation failed", zap.Error(err))
	}
	fetchService := services.NewFetchService(cfg, db, s3Client, z, enabledProviders)

	c := cron.New()
	c.AddFunc(cfg.CronSchedule, func() {
		z.Info("Starting scheduled fetch job.")
		count, err := fetchService.RunForAllSubstances(context.Background())
		if err != nil {
			z.Error("Scheduled fetch job failed", zap.Error(err))
		} else {
			z.Info("Scheduled fetch job completed", zap.Int("new_papers", count))
			newPapersCounter.Add(float64(count))
		}
	})
	c.Start()

	router := gin.New()
	router.Use(gin.Recovery(), gin.Logger())
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Registriere die API-Key-Middleware global
	router.Use(apiKeyAuthMiddleware(cfg))

	setupSubstanceRoutes(router, db)
	setupSearchFilterRoutes(router, db)
	setupPaperRoutes(router, db)
	setupSearchRoutes(router, db, fetchService, z)

	z.Info("Starting HTTP server", zap.String("port", cfg.HTTPPort))
	if err := router.Run(":" + cfg.HTTPPort); err != nil {
		z.Fatal("HTTP server failed", zap.Error(err))
	}
}

func setupSubstanceRoutes(router *gin.Engine, db *gorm.DB) {
	group := router.Group("/substances")
	{
		group.POST("", func(c *gin.Context) {
			var sub models.Substance
			if err := c.ShouldBindJSON(&sub); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			db.Create(&sub)
			c.JSON(http.StatusCreated, sub)
		})
		group.GET("", func(c *gin.Context) {
			var subs []models.Substance
			db.Find(&subs)
			c.JSON(http.StatusOK, subs)
		})
		group.DELETE("/:id", func(c *gin.Context) {
			db.Delete(&models.Substance{}, c.Param("id"))
			c.Status(http.StatusNoContent)
		})
	}
}

func setupSearchFilterRoutes(router *gin.Engine, db *gorm.DB) {
	group := router.Group("/search-filters")
	{
		group.POST("", func(c *gin.Context) {
			var filter models.SearchFilter
			if err := c.ShouldBindJSON(&filter); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			db.Create(&filter)
			c.JSON(http.StatusCreated, filter)
		})
		group.GET("", func(c *gin.Context) {
			var filters []models.SearchFilter
			db.Find(&filters)
			c.JSON(http.StatusOK, filters)
		})
		group.DELETE("/:id", func(c *gin.Context) {
			db.Delete(&models.SearchFilter{}, c.Param("id"))
			c.Status(http.StatusNoContent)
		})
	}
}

func setupPaperRoutes(router *gin.Engine, db *gorm.DB) {
	paperGroup := router.Group("/papers")
	{
		paperGroup.GET("", func(c *gin.Context) {
			var papers []models.Paper
			db.Find(&papers)
			c.JSON(http.StatusOK, papers)
		})
		paperGroup.GET("/:id", func(c *gin.Context) {
			var paper models.Paper
			if err := db.First(&paper, c.Param("id")).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "paper not found"})
				return
			}
			c.JSON(http.StatusOK, paper)
		})
	}
}

func setupSearchRoutes(router *gin.Engine, db *gorm.DB, fetchService *services.FetchService, logger *zap.Logger) {
	searchGroup := router.Group("/search")
	{
		searchGroup.POST("/substance/:id", func(c *gin.Context) {
			id, err := strconv.ParseUint(c.Param("id"), 10, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
				return
			}
			var sub models.Substance
			if err := db.First(&sub, id).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "substance not found"})
				return
			}
			var filters []models.SearchFilter
			db.Find(&filters)

			logger.Info("Async fetch for single substance triggered", zap.String("substance", sub.Name))
			go func() {
				count, err := fetchService.RunForSubstance(context.Background(), sub, filters)
				if err != nil {
					logger.Error("Async single fetch failed", zap.Error(err))
				} else {
					newPapersCounter.Add(float64(count))
					logger.Info("Async single fetch completed", zap.Int("new_papers", count), zap.String("substance", sub.Name))
				}
			}()
			c.JSON(http.StatusAccepted, gin.H{"status": "started for " + sub.Name})
		})

		searchGroup.POST("/all", func(c *gin.Context) {
			logger.Info("Async fetch for all substances triggered.")
			go func() {
				count, err := fetchService.RunForAllSubstances(context.Background())
				if err != nil {
					logger.Error("Async all-substance fetch failed", zap.Error(err))
				} else {
					newPapersCounter.Add(float64(count))
					logger.Info("Async all-substance fetch completed", zap.Int("total_new_papers", count))
				}
			}()
			c.JSON(http.StatusAccepted, gin.H{"status": "started for all substances"})
		})
	}
}

func seedDefaultSubstances(db *gorm.DB, logger *zap.Logger) {
	var count int64
	db.Model(&models.Substance{}).Count(&count)
	if count > 0 {
		return
	}

	subs := []models.Substance{
		{Name: "curcumin"},
		{Name: "bisdemethoxycurcumin"},
		{Name: "demethoxycurcumin"},
	}
	if err := db.Create(&subs).Error; err != nil {
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
		{Name: "Meta-Analysis (Human)", FilterQuery: `AND (meta-analysis[Title/Abstract] OR meta-analysis[Publication Type]) AND (human[Title/Abstract] OR humans[Title/Abstract])`},
		{Name: "RCT (Human)", FilterQuery: `AND (randomized controlled trial[Publication Type] OR randomized controlled trial[Title/Abstract]) AND (human[Title/Abstract] OR humans[Title/Abstract])`},
		{Name: "Review (Human)", FilterQuery: `AND (review[Publication Type]) AND (human[Title/Abstract] OR humans[Title/Abstract])`},
	}
	if err := db.Create(&filters).Error; err != nil {
		logger.Warn("Failed to seed default search filters", zap.Error(err))
	} else {
		logger.Info("Default search filters seeded.")
	}
}
