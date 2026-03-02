package handler

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ddevcap/jellymux/backend"
	"github.com/ddevcap/jellymux/config"
	"github.com/ddevcap/jellymux/ent"
	"github.com/gin-gonic/gin"
)

type SystemHandler struct {
	cfg          config.Config
	db           *ent.Client
	pool         *backend.Pool
	displayPrefs *displayPrefsStore
}

const jellyfinVersion = "10.11.6"

func NewSystemHandler(cfg config.Config, db *ent.Client, pool *backend.Pool) *SystemHandler {
	return &SystemHandler{cfg: cfg, db: db, pool: pool, displayPrefs: newDisplayPrefsStore()}
}

// InfoPublic handles GET /System/Info/Public (unauthenticated).
func (h *SystemHandler) InfoPublic(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"LocalAddress":           h.cfg.ExternalURL,
		"ServerName":             h.cfg.ServerName,
		"Version":                jellyfinVersion,
		"ProductName":            "Jellyfin Server",
		"OperatingSystem":        "Linux",
		"Id":                     dashlessID(h.cfg.ServerID),
		"StartupWizardCompleted": true,
	})
}

// Info handles GET /System/Info (authenticated).
// Capabilities that don't apply to a multi-backend proxy are explicitly false
// so the web UI does not render the corresponding admin buttons.
func (h *SystemHandler) Info(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"LocalAddress":           h.cfg.ExternalURL,
		"ServerName":             h.cfg.ServerName,
		"Version":                jellyfinVersion,
		"ProductName":            "Jellyfin Server",
		"OperatingSystem":        "Linux",
		"Id":                     dashlessID(h.cfg.ServerID),
		"StartupWizardCompleted": true,
		"SupportsLibraryMonitor": false,
		"CanSelfRestart":         false,
		"CanLaunchWebBrowser":    false,
		"HasUpdateAvailable":     false,
		"HasPendingRestart":      false,
		"EncoderLocation":        "NotFound",
		"SystemArchitecture":     "X64",
	})
}

func (h *SystemHandler) GetSystemLogs(c *gin.Context) {
	c.JSON(http.StatusOK, []interface{}{})
}

func (h *SystemHandler) GetSystemLogFile(c *gin.Context) {
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte{})
}

func (h *SystemHandler) GetPackages(c *gin.Context) {
	c.JSON(http.StatusOK, []interface{}{})
}

func (h *SystemHandler) GetRepositories(c *gin.Context) {
	c.JSON(http.StatusOK, []interface{}{})
}

func (h *SystemHandler) BrandingConfiguration(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"LoginDisclaimer":     "",
		"CustomCss":           "",
		"SplashscreenEnabled": false,
	})
}

// BrandingCss serves custom CSS.
func (h *SystemHandler) BrandingCss(c *gin.Context) {
	c.Data(http.StatusOK, "text/css; charset=utf-8", []byte(""))
}

// UsersPublic handles GET /Users/Public.
// Empty array = manual username entry required.
func (h *SystemHandler) UsersPublic(c *gin.Context) {
	c.JSON(http.StatusOK, []interface{}{})
}

// QuickConnectEnabled handles GET /QuickConnect/Enabled.
func (h *SystemHandler) QuickConnectEnabled(c *gin.Context) {
	c.JSON(http.StatusOK, false)
}

// QuickConnectInitiate handles POST /QuickConnect/Initiate.
// Not supported — returns 401 as a real Jellyfin server does when QC is disabled.
func (h *SystemHandler) QuickConnectInitiate(c *gin.Context) {
	c.JSON(http.StatusUnauthorized, gin.H{"error": "Quick connect is not enabled on this server"})
}

// SessionCapabilitiesFull handles POST /Sessions/Capabilities/Full.
// Acknowledged and discarded.
func (h *SystemHandler) SessionCapabilitiesFull(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// ClientLogDocument handles POST /ClientLog/Document.
// Logged at debug level, otherwise discarded.
func (h *SystemHandler) ClientLogDocument(c *gin.Context) {
	if slog.Default().Enabled(c.Request.Context(), slog.LevelDebug) {
		body, _ := io.ReadAll(io.LimitReader(c.Request.Body, 64*1024))
		if len(body) > 0 {
			user := userFromCtx(c)
			username := ""
			if user != nil {
				username = user.Username
			}
			slog.Debug("client log", "user", username, "body", string(body))
		}
	}
	c.Status(http.StatusOK)
}

// DisplayPreferencesGet handles GET /DisplayPreferences/{id}.
// Falls back to defaults if nothing has been saved yet.
func (h *SystemHandler) DisplayPreferencesGet(c *gin.Context) {
	id := c.Param("id")
	client := c.Query("client")
	user := userFromCtx(c)
	key := ""
	if user != nil {
		key = user.ID.String() + ":" + id + ":" + client
	}

	if key != "" {
		if stored, ok := h.displayPrefs.get(key); ok {
			c.Data(http.StatusOK, "application/json", stored)
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"Id":                 id,
		"SortBy":             "SortName",
		"RememberIndexing":   false,
		"PrimaryImageHeight": 250,
		"PrimaryImageWidth":  0,
		"CustomPrefs":        gin.H{},
		"ScrollDirection":    "Horizontal",
		"ShowBackdrop":       true,
		"RememberSorting":    false,
		"SortOrder":          "Ascending",
		"ShowSidebar":        false,
		"Client":             "emby",
		"IndexBy":            nil,
		"ViewType":           "",
	})
}

// DisplayPreferencesUpdate handles POST /DisplayPreferences/{id}.
func (h *SystemHandler) DisplayPreferencesUpdate(c *gin.Context) {
	id := c.Param("id")
	client := c.Query("client")
	user := userFromCtx(c)
	if user == nil {
		c.Status(http.StatusNoContent)
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodySize))
	if err != nil || len(body) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	key := user.ID.String() + ":" + id + ":" + client
	h.displayPrefs.set(key, body)
	c.Status(http.StatusNoContent)
}

// GetEndpointInfo handles GET /System/Endpoint.
func (h *SystemHandler) GetEndpointInfo(c *gin.Context) {
	ip := c.ClientIP()
	c.JSON(http.StatusOK, gin.H{
		"RemoteEndPoint": ip,
		"IsLocal":        true,
	})
}

// ActivityLogEntries handles GET /System/ActivityLog/Entries.
func (h *SystemHandler) ActivityLogEntries(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"Items": []interface{}{}, "TotalRecordCount": 0, "StartIndex": 0})
}

// InfoStorage handles GET /System/Info/Storage.
func (h *SystemHandler) InfoStorage(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"Drives": []interface{}{}})
}

// GetDevices handles GET /Devices.
func (h *SystemHandler) GetDevices(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"Items": []interface{}{}, "TotalRecordCount": 0, "StartIndex": 0})
}

// GetConfiguration handles GET /System/Configuration.
// Unsupported multi-backend admin features are explicitly disabled.
func (h *SystemHandler) GetConfiguration(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"LogFileRetentionDays":             3,
		"IsStartupWizardCompleted":         true,
		"EnableMetrics":                    false,
		"EnableNormalizedItemByNameIds":    false,
		"IsPortAuthorized":                 true,
		"QuickConnectAvailable":            false,
		"EnableCaseSensitiveItemIds":       true,
		"DisableLiveTvChannelUserDataName": true,
		"MetadataPath":                     "",
		"PreferredMetadataLanguage":        "en",
		"MetadataCountryCode":              "US",
		"SortReplaceCharacters":            []string{".", "+", "%"},
		"SortRemoveCharacters":             []string{"'", "!", "", "?"},
		"SortRemoveWords":                  []string{"the", "a", "an"},
		"MinResumePct":                     5,
		"MaxResumePct":                     90,
		"MinResumeDurationSeconds":         300,
		"LibraryMonitorDelay":              60,
		"ImageSavingConvention":            "Legacy",
		// Disable UI sections the proxy cannot support across multiple backends.
		"EnableFolderView":              false,
		"EnableGroupingIntoCollections": false,
		"DisplaySpecialsWithinSeasons":  true,
		"CodecsUsed":                    []string{},
		// Empty plugin repository list — plugins cannot be managed on the proxy.
		"PluginRepositories":                 []interface{}{},
		"EnableExternalContentInSuggestions": true,
		"RequireHttps":                       false,
		"EnableJavascriptLog":                false,
		"DisplayAnyDisclaimer":               false,
		"EnableSlowResponseWarning":          false,
		"SlowResponseThresholdMs":            500,
		"CorsHosts":                          []string{"*"},
		"ActivityLogRetentionDays":           30,
		"LibraryScanFanoutConcurrency":       0,
		"LibraryMetadataRefreshConcurrency":  0,
		"RemoveOldPlugins":                   false,
		"AllowClientLogUpload":               false,
	})
}

// GetConfigurationNetwork handles GET /System/Configuration/network.
func (h *SystemHandler) GetConfigurationNetwork(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"RequireHttps":              false,
		"EnableIPV4":                true,
		"EnableIPV6":                false,
		"EnableHttps":               false,
		"PublicHttpsPort":           8920,
		"HttpServerPortNumber":      8096,
		"HttpsPortNumber":           8920,
		"IsRemoteIPFilterBlacklist": false,
		"EnableRemoteAccess":        true,
		"RemoteIPFilter":            []string{},
		"LocalNetworkSubnets":       []string{},
		"LocalNetworkAddresses":     []string{},
		"KnownProxies":              []string{},
		"PublicPort":                8096,
		"AutoDiscovery":             false,
		"BaseUrl":                   "",
	})
}

// GetParentalRatings handles GET /ParentalRatings.
func (h *SystemHandler) GetParentalRatings(c *gin.Context) {
	c.JSON(http.StatusOK, []interface{}{})
}

// GetLocalizationOptions handles GET /Localization/Options.
func (h *SystemHandler) GetLocalizationOptions(c *gin.Context) {
	c.JSON(http.StatusOK, []interface{}{})
}

// cultures is the static list of cultures returned by /Localization/Cultures.
var cultures = []gin.H{
	{"Name": "English", "DisplayName": "English", "TwoLetterISOLanguageName": "en", "ThreeLetterISOLanguageName": "eng"},
	{"Name": "Afrikaans", "DisplayName": "Afrikaans", "TwoLetterISOLanguageName": "af", "ThreeLetterISOLanguageName": "afr"},
	{"Name": "Arabic", "DisplayName": "Arabic", "TwoLetterISOLanguageName": "ar", "ThreeLetterISOLanguageName": "ara"},
	{"Name": "Bulgarian", "DisplayName": "Bulgarian", "TwoLetterISOLanguageName": "bg", "ThreeLetterISOLanguageName": "bul"},
	{"Name": "Catalan", "DisplayName": "Catalan", "TwoLetterISOLanguageName": "ca", "ThreeLetterISOLanguageName": "cat"},
	{"Name": "Chinese (Simplified)", "DisplayName": "Chinese (Simplified)", "TwoLetterISOLanguageName": "zh", "ThreeLetterISOLanguageName": "zho"},
	{"Name": "Chinese (Traditional)", "DisplayName": "Chinese (Traditional)", "TwoLetterISOLanguageName": "zh-TW", "ThreeLetterISOLanguageName": "zht"},
	{"Name": "Croatian", "DisplayName": "Croatian", "TwoLetterISOLanguageName": "hr", "ThreeLetterISOLanguageName": "hrv"},
	{"Name": "Czech", "DisplayName": "Czech", "TwoLetterISOLanguageName": "cs", "ThreeLetterISOLanguageName": "ces"},
	{"Name": "Danish", "DisplayName": "Danish", "TwoLetterISOLanguageName": "da", "ThreeLetterISOLanguageName": "dan"},
	{"Name": "Dutch", "DisplayName": "Dutch", "TwoLetterISOLanguageName": "nl", "ThreeLetterISOLanguageName": "nld"},
	{"Name": "Finnish", "DisplayName": "Finnish", "TwoLetterISOLanguageName": "fi", "ThreeLetterISOLanguageName": "fin"},
	{"Name": "French", "DisplayName": "French", "TwoLetterISOLanguageName": "fr", "ThreeLetterISOLanguageName": "fra"},
	{"Name": "German", "DisplayName": "German", "TwoLetterISOLanguageName": "de", "ThreeLetterISOLanguageName": "deu"},
	{"Name": "Greek", "DisplayName": "Greek", "TwoLetterISOLanguageName": "el", "ThreeLetterISOLanguageName": "ell"},
	{"Name": "Hebrew", "DisplayName": "Hebrew", "TwoLetterISOLanguageName": "he", "ThreeLetterISOLanguageName": "heb"},
	{"Name": "Hindi", "DisplayName": "Hindi", "TwoLetterISOLanguageName": "hi", "ThreeLetterISOLanguageName": "hin"},
	{"Name": "Hungarian", "DisplayName": "Hungarian", "TwoLetterISOLanguageName": "hu", "ThreeLetterISOLanguageName": "hun"},
	{"Name": "Icelandic", "DisplayName": "Icelandic", "TwoLetterISOLanguageName": "is", "ThreeLetterISOLanguageName": "isl"},
	{"Name": "Indonesian", "DisplayName": "Indonesian", "TwoLetterISOLanguageName": "id", "ThreeLetterISOLanguageName": "ind"},
	{"Name": "Italian", "DisplayName": "Italian", "TwoLetterISOLanguageName": "it", "ThreeLetterISOLanguageName": "ita"},
	{"Name": "Japanese", "DisplayName": "Japanese", "TwoLetterISOLanguageName": "ja", "ThreeLetterISOLanguageName": "jpn"},
	{"Name": "Korean", "DisplayName": "Korean", "TwoLetterISOLanguageName": "ko", "ThreeLetterISOLanguageName": "kor"},
	{"Name": "Latvian", "DisplayName": "Latvian", "TwoLetterISOLanguageName": "lv", "ThreeLetterISOLanguageName": "lav"},
	{"Name": "Lithuanian", "DisplayName": "Lithuanian", "TwoLetterISOLanguageName": "lt", "ThreeLetterISOLanguageName": "lit"},
	{"Name": "Malay", "DisplayName": "Malay", "TwoLetterISOLanguageName": "ms", "ThreeLetterISOLanguageName": "msa"},
	{"Name": "Norwegian", "DisplayName": "Norwegian", "TwoLetterISOLanguageName": "no", "ThreeLetterISOLanguageName": "nor"},
	{"Name": "Persian", "DisplayName": "Persian", "TwoLetterISOLanguageName": "fa", "ThreeLetterISOLanguageName": "fas"},
	{"Name": "Polish", "DisplayName": "Polish", "TwoLetterISOLanguageName": "pl", "ThreeLetterISOLanguageName": "pol"},
	{"Name": "Portuguese", "DisplayName": "Portuguese", "TwoLetterISOLanguageName": "pt", "ThreeLetterISOLanguageName": "por"},
	{"Name": "Romanian", "DisplayName": "Romanian", "TwoLetterISOLanguageName": "ro", "ThreeLetterISOLanguageName": "ron"},
	{"Name": "Russian", "DisplayName": "Russian", "TwoLetterISOLanguageName": "ru", "ThreeLetterISOLanguageName": "rus"},
	{"Name": "Serbian", "DisplayName": "Serbian", "TwoLetterISOLanguageName": "sr", "ThreeLetterISOLanguageName": "srp"},
	{"Name": "Slovak", "DisplayName": "Slovak", "TwoLetterISOLanguageName": "sk", "ThreeLetterISOLanguageName": "slk"},
	{"Name": "Slovenian", "DisplayName": "Slovenian", "TwoLetterISOLanguageName": "sl", "ThreeLetterISOLanguageName": "slv"},
	{"Name": "Spanish", "DisplayName": "Spanish", "TwoLetterISOLanguageName": "es", "ThreeLetterISOLanguageName": "spa"},
	{"Name": "Swedish", "DisplayName": "Swedish", "TwoLetterISOLanguageName": "sv", "ThreeLetterISOLanguageName": "swe"},
	{"Name": "Thai", "DisplayName": "Thai", "TwoLetterISOLanguageName": "th", "ThreeLetterISOLanguageName": "tha"},
	{"Name": "Turkish", "DisplayName": "Turkish", "TwoLetterISOLanguageName": "tr", "ThreeLetterISOLanguageName": "tur"},
	{"Name": "Ukrainian", "DisplayName": "Ukrainian", "TwoLetterISOLanguageName": "uk", "ThreeLetterISOLanguageName": "ukr"},
	{"Name": "Vietnamese", "DisplayName": "Vietnamese", "TwoLetterISOLanguageName": "vi", "ThreeLetterISOLanguageName": "vie"},
}

// countries is the static list of countries returned by /Localization/Countries.
var countries = []gin.H{
	{"Name": "AUS", "DisplayName": "Australia", "TwoLetterISORegionName": "AU", "ThreeLetterISORegionName": "AUS"},
	{"Name": "AUT", "DisplayName": "Austria", "TwoLetterISORegionName": "AT", "ThreeLetterISORegionName": "AUT"},
	{"Name": "BEL", "DisplayName": "Belgium", "TwoLetterISORegionName": "BE", "ThreeLetterISORegionName": "BEL"},
	{"Name": "BRA", "DisplayName": "Brazil", "TwoLetterISORegionName": "BR", "ThreeLetterISORegionName": "BRA"},
	{"Name": "CAN", "DisplayName": "Canada", "TwoLetterISORegionName": "CA", "ThreeLetterISORegionName": "CAN"},
	{"Name": "CHN", "DisplayName": "China", "TwoLetterISORegionName": "CN", "ThreeLetterISORegionName": "CHN"},
	{"Name": "CZE", "DisplayName": "Czech Republic", "TwoLetterISORegionName": "CZ", "ThreeLetterISORegionName": "CZE"},
	{"Name": "DNK", "DisplayName": "Denmark", "TwoLetterISORegionName": "DK", "ThreeLetterISORegionName": "DNK"},
	{"Name": "FIN", "DisplayName": "Finland", "TwoLetterISORegionName": "FI", "ThreeLetterISORegionName": "FIN"},
	{"Name": "FRA", "DisplayName": "France", "TwoLetterISORegionName": "FR", "ThreeLetterISORegionName": "FRA"},
	{"Name": "DEU", "DisplayName": "Germany", "TwoLetterISORegionName": "DE", "ThreeLetterISORegionName": "DEU"},
	{"Name": "GRC", "DisplayName": "Greece", "TwoLetterISORegionName": "GR", "ThreeLetterISORegionName": "GRC"},
	{"Name": "HUN", "DisplayName": "Hungary", "TwoLetterISORegionName": "HU", "ThreeLetterISORegionName": "HUN"},
	{"Name": "IND", "DisplayName": "India", "TwoLetterISORegionName": "IN", "ThreeLetterISORegionName": "IND"},
	{"Name": "IRL", "DisplayName": "Ireland", "TwoLetterISORegionName": "IE", "ThreeLetterISORegionName": "IRL"},
	{"Name": "ISR", "DisplayName": "Israel", "TwoLetterISORegionName": "IL", "ThreeLetterISORegionName": "ISR"},
	{"Name": "ITA", "DisplayName": "Italy", "TwoLetterISORegionName": "IT", "ThreeLetterISORegionName": "ITA"},
	{"Name": "JPN", "DisplayName": "Japan", "TwoLetterISORegionName": "JP", "ThreeLetterISORegionName": "JPN"},
	{"Name": "KOR", "DisplayName": "South Korea", "TwoLetterISORegionName": "KR", "ThreeLetterISORegionName": "KOR"},
	{"Name": "MEX", "DisplayName": "Mexico", "TwoLetterISORegionName": "MX", "ThreeLetterISORegionName": "MEX"},
	{"Name": "NLD", "DisplayName": "Netherlands", "TwoLetterISORegionName": "NL", "ThreeLetterISORegionName": "NLD"},
	{"Name": "NZL", "DisplayName": "New Zealand", "TwoLetterISORegionName": "NZ", "ThreeLetterISORegionName": "NZL"},
	{"Name": "NOR", "DisplayName": "Norway", "TwoLetterISORegionName": "NO", "ThreeLetterISORegionName": "NOR"},
	{"Name": "POL", "DisplayName": "Poland", "TwoLetterISORegionName": "PL", "ThreeLetterISORegionName": "POL"},
	{"Name": "PRT", "DisplayName": "Portugal", "TwoLetterISORegionName": "PT", "ThreeLetterISORegionName": "PRT"},
	{"Name": "RUS", "DisplayName": "Russia", "TwoLetterISORegionName": "RU", "ThreeLetterISORegionName": "RUS"},
	{"Name": "ZAF", "DisplayName": "South Africa", "TwoLetterISORegionName": "ZA", "ThreeLetterISORegionName": "ZAF"},
	{"Name": "ESP", "DisplayName": "Spain", "TwoLetterISORegionName": "ES", "ThreeLetterISORegionName": "ESP"},
	{"Name": "SWE", "DisplayName": "Sweden", "TwoLetterISORegionName": "SE", "ThreeLetterISORegionName": "SWE"},
	{"Name": "CHE", "DisplayName": "Switzerland", "TwoLetterISORegionName": "CH", "ThreeLetterISORegionName": "CHE"},
	{"Name": "TWN", "DisplayName": "Taiwan", "TwoLetterISORegionName": "TW", "ThreeLetterISORegionName": "TWN"},
	{"Name": "TUR", "DisplayName": "Turkey", "TwoLetterISORegionName": "TR", "ThreeLetterISORegionName": "TUR"},
	{"Name": "UKR", "DisplayName": "Ukraine", "TwoLetterISORegionName": "UA", "ThreeLetterISORegionName": "UKR"},
	{"Name": "GBR", "DisplayName": "United Kingdom", "TwoLetterISORegionName": "GB", "ThreeLetterISORegionName": "GBR"},
	{"Name": "USA", "DisplayName": "United States", "TwoLetterISORegionName": "US", "ThreeLetterISORegionName": "USA"},
}

// GetLocalizationCultures handles GET /Localization/Cultures.
func (h *SystemHandler) GetLocalizationCultures(c *gin.Context) {
	c.JSON(http.StatusOK, cultures)
}

// GetLocalizationCountries handles GET /Localization/Countries.
func (h *SystemHandler) GetLocalizationCountries(c *gin.Context) {
	c.JSON(http.StatusOK, countries)
}

// BitrateTest handles GET /Playback/BitrateTest.
// Returns Size bytes of zeroes for bandwidth measurement.
func (h *SystemHandler) BitrateTest(c *gin.Context) {
	size, err := strconv.ParseInt(c.Query("Size"), 10, 64)
	if err != nil || size <= 0 {
		size = 102400 // default 100 KB
	}
	// Cap at 10 MB to prevent abuse.
	const maxSize = 10 * 1024 * 1024
	if size > maxSize {
		size = maxSize
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Status(http.StatusOK)

	// Stream zeroes in 32 KB chunks to limit per-request memory.
	const chunkSize = 32 * 1024
	chunk := make([]byte, chunkSize)
	remaining := size
	for remaining > 0 {
		n := int64(chunkSize)
		if remaining < n {
			n = remaining
		}
		if _, err := c.Writer.Write(chunk[:n]); err != nil {
			return
		}
		remaining -= n
	}
}

// HealthLive handles GET /health (liveness probe).
func (h *SystemHandler) HealthLive(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// HealthReady handles GET /ready (readiness probe — checks DB connectivity).
func (h *SystemHandler) HealthReady(c *gin.Context) {
	// Quick DB ping.
	if _, err := h.db.User.Query().Limit(1).Count(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "error": "database unreachable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
