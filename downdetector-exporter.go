// downdetector-exporter is a Prometheus exporter for Downdetector metrics.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// a token can live at most 3600 seconds before it needs to be refreshed.
	tokenGraceSeconds = 300 // Seconds before token EOL when a new token must be fetched
	// seconds before next loop is started.
	minSleepSeconds = 60
	baseURL         = "https://downdetectorapi.com/v2"
)

var (
	// version is set via ldflags during build.
	version = "dev"
	logger  = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError, AddSource: true}))

	// fields for metrics request. If expanded, struct CompanySet needs to be expanded accordingly.
	fieldsToReturn       = []string{"id", "name", "slug", "baseline_current", "country_iso", "stats_24", "stats_60", "status"}
	fieldsToReturnSearch = []string{"id", "name", "slug", "country_iso"}

	token       Token
	credentials BasicAuth
	username    string
	password    string

	httpClient *http.Client

	// Downdetector delivers one CompanySet per given ID.
	metricsResponse []CompanySet

	// exposed holds the various metrics that are collected.
	exposed = map[string]*prometheus.GaugeVec{}
	// show last update time to see if system is working correctly.
	lastUpdate = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dd_lastUpdate",
		Help: "Last update timestamp in epoch seconds",
	},
		[]string{"scope"},
	)
)

func init() {
	// add the lastUpdate metrics to prometheus
	prometheus.MustRegister(lastUpdate)
}

// BasicAuth contains API credentials and optional company IDs loaded from YAML.
type BasicAuth struct {
	UserName   string `json:"username" yaml:"username"`
	Password   string `json:"password" yaml:"password"`
	CompanyIDs string `json:"company_ids" yaml:"company_ids"`
}

// Token contains token, expiration at issuing time, type and, later, the time of issuing.
type Token struct {
	// Access containing access token (type Bearer normally)
	Access string `json:"access_token"`
	// ExpiresIn usually contains 3600 (seconds)
	ExpiresIn int `json:"expires_in"`
	// Type contains the token type (Bearer)
	Type string `json:"token_type"`
	// RefreshTime must programmatically be set after a token has been successfully fetched
	RefreshTime time.Time
}

// CompanySet contains returned data per Company
// CompanySet Prefix field with Label if value is to be used as label
// CompanySet Prefix field with Ignore if value is neither a metric nor a label but you want to handle it programmatically
// CompanySet Fields without Prefix will be used as metrics value.
type CompanySet struct {
	LabelCountryISO string `json:"country_iso,omitempty"`
	LabelName       string `json:"name,omitempty"`
	LabelSlug       string `json:"slug,omitempty"`
	// IgnoreStatus contains the status name (success, warning, danger) in string form
	IgnoreStatus string `json:"status,omitempty"`
	LabelID      int    `json:"id"`
	// BaseLineCurrent is a value generated over the last 24 hours, shows the normal baseline value of a service
	BaselineCurrent int `json:"baseline_current"`
	// Stats60 is the current metrics of mentions
	Stats60 int `json:"stats_60"`
	// IgnoreStats24 is the statistics over the last 24h in 15 minute buckets.
	IgnoreStats24 []int `json:"stats_24"`
	// Stats15 is the number of reports over the last 15
	Stats15 int `json:"-"`
	// NumStatus needs to be filled in programmatically from IgnoreStatus value so it can be used as metric
	NumStatus int `json:"-"`
}

func loadCredentials(credentialsFile string) (BasicAuth, error) {
	file, err := os.Open(credentialsFile)
	if err != nil {
		return BasicAuth{}, fmt.Errorf("couldn't read credentials file: %w", err)
	}
	var loaded BasicAuth
	if err := yaml.NewDecoder(file).Decode(&loaded); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return BasicAuth{}, fmt.Errorf("couldn't close credentials file after parsing error: %w", closeErr)
		}
		return BasicAuth{}, fmt.Errorf("couldn't parse credentials file: %w", err)
	}
	if err := file.Close(); err != nil {
		return BasicAuth{}, fmt.Errorf("couldn't close credentials file: %w", err)
	}
	return loaded, nil
}

func main() {
	var listenAddress string
	var credentialsFile string
	var metricsPath string
	var logLevel string
	var companyIDs string
	var searchString string
	var showVersion bool

	flagAliases := map[string]string{
		"company_ids":      "i",
		"credentials_file": "c",
		"username":         "u",
		"password":         "p",
		"listen_address":   "l",
		"metrics_path":     "m",
		"log_level":        "v",
		"search_string":    "s",
		"version":          "V",
	}
	flagDefaults := map[string]string{
		"listen_address": ":9313",
		"metrics_path":   "/metrics",
		"log_level":      "ERROR",
	}
	flags := flag.NewFlagSet("downdetector-exporter", flag.ContinueOnError)
	flags.Usage = func() {
		var help strings.Builder
		help.WriteString("Usage: downdetector-exporter [options]\n")
		help.WriteString("Report metrics of the Downdetector API.\n\nOptions:\n")
		flags.VisitAll(func(f *flag.Flag) {
			if len(f.Name) == 1 {
				return
			}
			help.WriteString("  --" + f.Name)
			if alias := flagAliases[f.Name]; alias != "" {
				help.WriteString(", -" + alias)
			}
			if _, isBool := f.Value.(interface{ IsBoolFlag() bool }); !isBool {
				help.WriteString(" <value>")
			}
			help.WriteString("\t" + f.Usage)
			if defaultValue, ok := flagDefaults[f.Name]; ok {
				help.WriteString(" (default " + strconv.Quote(defaultValue) + ")")
			}
			help.WriteString("\n")
		})
		if _, err := io.WriteString(flags.Output(), help.String()); err != nil {
			logger.Error("could not write command-line help", "error", err)
		}
	}

	addStringFlag(flags, &companyIDs, "company_ids", "i", "COMPANY_IDS", "", "comma separated list of company ids to monitor")
	addStringFlag(flags, &credentialsFile, "credentials_file", "c", "CREDENTIALS_FILE", "", "credentials YAML file; username and password flags take precedence")
	addStringFlag(flags, &username, "username", "u", "DD_USERNAME", "", "username, takes precedence over credentials file")
	addStringFlag(flags, &password, "password", "p", "DD_PASSWORD", "", "password, takes precedence over credentials file")
	addStringFlag(flags, &listenAddress, "listen_address", "l", "LISTEN_ADDRESS", ":9313", "address to listen on, either :port or address:port")
	addStringFlag(flags, &metricsPath, "metrics_path", "m", "METRICS_PATH", "/metrics", "URL path where metrics are exposed")
	addStringFlag(flags, &logLevel, "log_level", "v", "LOG_LEVEL", "ERROR", "log level: DEBUG, INFO, WARN, ERROR")
	addStringFlag(flags, &searchString, "search_string", "s", "", "", "search for companies containing this text and return their IDs")
	flags.BoolVar(&showVersion, "version", false, "print version information and exit")
	flags.BoolVar(&showVersion, "V", false, "print version information and exit")

	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		logger.Error("could not parse command-line flags", "error", err)
		os.Exit(2)
	}
	if showVersion {
		fmt.Printf("downdetector-exporter version %s\n", version)
		return
	}

	configureLogger(logLevel)

	if credentialsFile != "" && (username == "" || password == "" || (companyIDs == "" && searchString == "")) {
		loadedCredentials, err := loadCredentials(credentialsFile)
		if err != nil {
			logger.Error("could not load credentials", "error", err)
			os.Exit(2)
		}
		credentials = loadedCredentials
	}
	if username != "" && password != "" {
		credentials.UserName = username
		credentials.Password = password
	}
	if credentials.UserName == "" || credentials.Password == "" {
		logger.Error("Either credentials_file or username and password need to be set!")
		os.Exit(2)
	}
	if companyIDs == "" {
		companyIDs = credentials.CompanyIDs
	}
	if companyIDs == "" && searchString == "" {
		logger.Error("Either company_ids or a search string need to be set!")
		os.Exit(2)
	}

	logger.Debug("configuration", "listenAddress", listenAddress, "credentialsFile", credentialsFile, "metricsPath", metricsPath, "companyIDs", companyIDs)

	// install promhttp handler for metricsPath (/metrics)
	http.Handle(metricsPath, promhttp.Handler())

	// show nice web page if called without metricsPath
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`<html>
					<head><title>Downdetector Exporter</title></head>
					<body>
					<h1>Downdetector Exporter</h1>
					<p><a href='` + metricsPath + `'>Metrics</a></p>
					</body>
					</html>`)); err != nil {
			logger.Warn("couldn't write response: " + err.Error())
		}
	})

	// Start the http server in background, but catch error
	go func() {
		err := http.ListenAndServe(listenAddress, nil)
		logger.Error(err.Error())
		os.Exit(2)
	}()

	// wait for initialization of http server before the local liveness check
	time.Sleep(time.Second * 3)

	// TODO: Proxy URL instead of ""
	httpClient = getHTTPClient("")

	// Working loop
	for {
		// does the individual work, so the rest of the code can be used for other exporters
		workHorse(companyIDs, searchString)

		// check the local metrics endpoint
		systemAlive(listenAddress, metricsPath)

		// sleep minSleepSeconds seconds before starting next loop
		time.Sleep(time.Second * minSleepSeconds)
	}
}

func addStringFlag(flags *flag.FlagSet, target *string, name, alias, envName, defaultValue, usage string) {
	if envName != "" {
		if value, ok := os.LookupEnv(envName); ok {
			defaultValue = value
		}
		usage += " [$" + envName + "]"
	}
	flags.StringVar(target, name, defaultValue, usage)
	if alias != "" {
		flags.StringVar(target, alias, defaultValue, "")
	}
}

func configureLogger(logLevel string) {
	var level slog.Level
	switch strings.ToUpper(logLevel) {
	case "DEBUG":
		level = slog.LevelDebug
	case "INFO":
		level = slog.LevelInfo
	case "WARN":
		level = slog.LevelWarn
	default:
		level = slog.LevelError
	}

	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level:     level,
		AddSource: true,
	}))
}

func workHorse(companyIDs string, searchString string) {
	// refresh token if only tokenGraceSeconds are left before it expires
	if token.Access == "" || int(time.Since(token.RefreshTime).Seconds()) > token.ExpiresIn-tokenGraceSeconds {
		logger.Debug("refreshing token")
		initToken()
	} else {
		logger.Debug(fmt.Sprintf("Seconds before a new token must be fetched: %d", (token.ExpiresIn-tokenGraceSeconds)-int(time.Since(token.RefreshTime).Seconds())))
	}

	if getMetrics(companyIDs, searchString) {
		os.Exit(2)
	}
}

func getHTTPClient(proxyURLStr string) *http.Client {
	var (
		httpRequestsTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "client_api_requests_total",
				Help: "Total number of client requests made.",
			},
			[]string{"method", "code"},
		)
	)
	prometheus.MustRegister(httpRequestsTotal)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	if proxyURLStr != "" {
		proxyURL, err := url.Parse(proxyURLStr)
		if err != nil {
			logger.Error("Couldn't parse proxy url: " + err.Error())
			os.Exit(2)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	roundTripper := promhttp.InstrumentRoundTripperCounter(httpRequestsTotal, transport)

	// adding the Transport object to the http Client
	client := &http.Client{
		Transport: roundTripper,
		Timeout:   60 * time.Second,
	}
	return client
}

func initToken() {
	// create the token refresh request
	url := baseURL + "/tokens?grant_type=client_credentials"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	req.SetBasicAuth(credentials.UserName, credentials.Password)
	if err != nil {
		// return if we weren't successful - we have tokenGraceSeconds to retry
		logger.Warn("Couldn't apply Basic Auth: " + err.Error())
		return
	}
	// send the token refresh request
	res, err := httpClient.Do(req)
	if err != nil {
		logger.Error("Couldn't get token: " + err.Error())
		return
	}
	if res.StatusCode != http.StatusOK {
		// return if we weren't successful - we have tokenGraceSeconds to retry
		body, _ := io.ReadAll(res.Body)
		logger.Warn(fmt.Sprintf("Error response code: %d - %s", res.StatusCode, body))
		return
	}
	defer func() { _ = res.Body.Close() }()

	// read body from response
	body, err := io.ReadAll(res.Body)
	if err != nil {
		// return if we weren't successful - we have tokenGraceSeconds to retry
		logger.Warn("Couldn't read in body: " + err.Error())
		return
	}

	// unmarshal body content into token struct
	err = json.Unmarshal(body, &token)
	if err != nil {
		logger.Warn("Couldn't unmarshal json: " + err.Error())
		return
	}

	// Mark we have refreshed token right now
	token.RefreshTime = time.Now()
	logger.Debug("Token Type: " + token.Type)
	logger.Debug(fmt.Sprintf("Expires in: %d", token.ExpiresIn))
	logger.Debug(fmt.Sprintf("Token Refresh Time: %s", token.RefreshTime))
}

// getMetrics fetches metrics or search results and reports whether the caller should exit afterwards.
func getMetrics(companyIDs string, searchString string) bool {
	var url string
	if searchString == "" {
		// create the metrics fetching request
		url = baseURL + "/companies?fields=" + strings.Join(fieldsToReturn, "%2C") + "&ids=" + strings.ReplaceAll(companyIDs, ",", "%2C")
	} else {
		url = baseURL + "/companies/search?name=" + searchString + "&fields=" + strings.Join(fieldsToReturnSearch, "%2C")
	}
	// curl --request GET -H "Authorization: Bearer $TOKEN" --url 'https://downdetectorapi.com/v2/companies/search?name=mail.com&fields=url%2Cbaseline%2Csite_id%2Cstatus%2Ccountry_iso%2Cname%2Cslug' | jq .

	req, err := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Add("Authorization", "Bearer "+token.Access)
	if err != nil {
		logger.Warn("Couldn't apply authorization header: " + err.Error())
		return false
	}
	// send the metrics request
	res, err := httpClient.Do(req)
	if err != nil {
		logger.Error("Couldn't get metrics: " + err.Error())
		return false
	}
	if res.StatusCode != http.StatusOK {
		// return if we weren't successful
		body, _ := io.ReadAll(res.Body)
		logger.Warn(fmt.Sprintf("Could not get metrics: %d - %s", res.StatusCode, body))
		return false
	}
	defer func() { _ = res.Body.Close() }()

	// read body from response
	body, err := io.ReadAll(res.Body)
	if err != nil {
		// return if we weren't successful - we have tokenGraceSeconds to retry
		logger.Warn("Couldn't read in body: " + err.Error())
		return false
	}

	// unmarshal body content into metricResponse struct
	err = json.Unmarshal(body, &metricsResponse)
	if err != nil {
		logger.Warn("Couldn't unmarshal json: " + err.Error())
		return false
	}

	// Loop through all companies in response
	for _, companySet := range metricsResponse {
		if searchString != "" {
			fmt.Printf("ID: %d - Name: %s, Slug: %s, Country: %s\n", companySet.LabelID, companySet.LabelName, companySet.LabelSlug, companySet.LabelCountryISO)
		} else {
			// convert string value (success, warning, danger) to int metrics
			switch companySet.IgnoreStatus {
			case "success":
				companySet.NumStatus = 0
			case "warning":
				companySet.NumStatus = 1
			case "danger":
				companySet.NumStatus = 2
			default:
				companySet.NumStatus = -1
			}
			// get last value from Stats24 array
			companySet.Stats15 = companySet.IgnoreStats24[len(companySet.IgnoreStats24)-1]

			// Debugging output
			logger.Debug("")
			logger.Debug("===== Labels =====")
			logger.Debug("Name:             " + companySet.LabelName)
			logger.Debug("Slug:             " + companySet.LabelSlug)
			logger.Debug("Country:          " + companySet.LabelCountryISO)
			logger.Debug(fmt.Sprintf("Name:             %d", companySet.LabelID))
			logger.Debug("===== Info =====")
			logger.Debug("Status:           " + companySet.IgnoreStatus)
			logger.Debug("===== Metrics =====")
			logger.Debug(fmt.Sprintf("Current Baseline: %d", companySet.BaselineCurrent))
			logger.Debug(fmt.Sprintf("Stats60:          %d", companySet.Stats60))
			logger.Debug(fmt.Sprintf("Stats15:          %d", companySet.Stats15))
			logger.Debug(fmt.Sprintf("Status:           %d", companySet.NumStatus))

			// create empty array to hold labels
			labels := make([]string, 0)
			// create empty array to hold label values
			labelValues := make([]string, 0)

			// reflect to get members of struct
			cs := reflect.ValueOf(companySet)
			typeOfCompanySet := cs.Type()

			// Loop over all struct members and collect all fields starting with Label in array of labels
			logger.Debug("")
			logger.Debug("Looping over CompanySet")

			for i := 0; i < cs.NumField(); i++ {
				key := typeOfCompanySet.Field(i).Name
				value := cs.Field(i).Interface()
				logger.Debug(fmt.Sprintf("Field: %s, Value: %v", key, value))
				if after, ok := strings.CutPrefix(key, "Label"); ok {
					// labels have lower case names
					labels = append(labels, strings.ToLower(after))
					var labelValue string
					// IDs are returned as integers, convert to string
					if cs.Field(i).Type().Name() == "string" {
						labelValue = cs.Field(i).String()
					} else {
						labelValue = strconv.FormatInt(cs.Field(i).Int(), 10)
					}
					labelValues = append(labelValues, labelValue)
				}
			}
			logger.Debug("")
			logger.Debug(fmt.Sprintf("Labels: %v", labels))

			// Loop over all struct fields and set Exporter to value with list of labels if they don't
			// start with Label or Ignore
			logger.Debug("")
			for i := 0; i < cs.NumField(); i++ {
				key := typeOfCompanySet.Field(i).Name
				if !strings.HasPrefix(key, "Label") && !strings.HasPrefix(key, "Ignore") {
					value := cs.Field(i).Int()
					setPrometheusMetric(key, int(value), labels, labelValues)
				}
			}
		}
	}
	return searchString != ""
}

func setPrometheusMetric(key string, value int, labels []string, labelValues []string) {
	logger.Debug(fmt.Sprintf("Key: %s, Value: %d, Labels: %v", key, value, labels))
	// Check if metric is already registered, if not, register it
	_, ok := exposed[key]
	if !ok {
		exposed[key] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dd_" + key,
			Help: "N/A",
		},
			labels,
		)

		prometheus.MustRegister(exposed[key])
	}

	// Now set the value
	exposed[key].WithLabelValues(labelValues...).Set(float64(value))

	// Update lastUpdate so we immediately see when no updates happen anymore
	now := time.Now()
	seconds := now.Unix()
	lastUpdate.WithLabelValues("global").Set(float64(seconds))
}

func systemAlive(listenAddress string, metricsPath string) {
	// local liveness check
	var metricsURL string
	if !strings.HasPrefix(listenAddress, ":") {
		// User has provided address + port
		metricsURL = "http://" + listenAddress + metricsPath
	} else {
		// User has provided :port only - we need to check ourselves on 127.0.0.1
		metricsURL = "http://127.0.0.1" + listenAddress + metricsPath
	}

	// Call the metrics URL...
	res, err := http.Get(metricsURL)
	if err != nil {
		// ... log if the local metrics endpoint is unavailable.
		logger.Warn("liveness check failed: " + err.Error())
		return
	}
	// Read all away or else we'll run out of sockets sooner or later
	_, _ = io.ReadAll(res.Body)
	if err := res.Body.Close(); err != nil {
		logger.Warn("couldn't close response body: " + err.Error())
	}
}
