package monitoring

// Profile represents a single monitoring alert profile with metric and log alerts.
type Profile struct {
	MetricAlerts map[string]interface{} `yaml:"metric_alerts" json:"metric_alerts"`
	LogAlerts    map[string]interface{} `yaml:"log_alerts" json:"log_alerts"`
}
