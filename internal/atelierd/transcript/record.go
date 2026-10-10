package transcript

type Record struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp,omitempty"`
	AiTitle     string `json:"aiTitle,omitempty"`
	CustomTitle string `json:"customTitle,omitempty"`
}
