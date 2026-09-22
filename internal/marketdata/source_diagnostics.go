package marketdata

import (
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

func sourceDiagnostic(provider, ticker, stage, reason, disposition, message string) model.SourceDiagnostic {
	d := model.SourceDiagnostic{Provider: redact.String(provider), Ticker: ticker, Stage: stage, Reason: reason, Disposition: disposition, Message: redact.String(message)}
	if ticker != "" {
		d.Region = ResearchRegion(ticker)
	}
	d.ID = truncatedHexID(d.Provider+"\x00"+ticker+"\x00"+stage+"\x00"+reason+"\x00"+disposition+"\x00"+d.Message, 20)
	return d
}

// DocumentDiagnostics reports accessibility and discovery-only pages separately.
func DocumentDiagnostics(d model.EvidenceDocument) []model.SourceDiagnostic {
	if d.Error != "" {
		return []model.SourceDiagnostic{sourceDiagnostic(d.URL, d.Ticker, "document", "fetch_failed", "failed", d.Error)}
	}
	if d.NavigationOnly || ResearchNavigationURL(d.URL) {
		return []model.SourceDiagnostic{sourceDiagnostic(d.URL, d.Ticker, "document", "navigation_only", "context", "discovery page; not substantive evidence")}
	}
	return nil
}
