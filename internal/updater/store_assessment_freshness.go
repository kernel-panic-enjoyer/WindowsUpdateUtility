package updater

import (
	"fmt"
	"strings"
	"time"
)

// storeAssessmentFreshnessWindow is the maximum age of published Store evidence
// that may authorize an update. Store offers and installed AppX package
// versions can change outside this process; two hours is long enough for a
// normal WebUI session while preventing day-old or recovered evidence from
// becoming executable.
const storeAssessmentFreshnessWindow = 2 * time.Hour

type storePublishedAssessmentFreshness struct {
	Fresh  bool
	Reason string
}

func evaluatePublishedStoreAssessmentFreshness(snapshot StoreScanSnapshot, assessment StorePublishedAssessment, currentInstalledVersion string, now time.Time) storePublishedAssessmentFreshness {
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !snapshot.Published {
		return stalePublishedStoreAssessmentFreshness("snapshot was not published")
	}
	if snapshot.RecoveredFromFallback {
		return stalePublishedStoreAssessmentFreshness("snapshot was recovered after a newer snapshot could not be decoded")
	}
	if assessment.Stale {
		return stalePublishedStoreAssessmentFreshness("assessment was retained from an earlier scan")
	}
	if staleReason := staleStoreEvidenceTimeReason("snapshot", snapshot.Scan.CompletedAt, now); staleReason != "" {
		return stalePublishedStoreAssessmentFreshness(staleReason)
	}
	if staleReason := staleStoreEvidenceTimeReason("assessment", assessment.ObservedAt, now); staleReason != "" {
		return stalePublishedStoreAssessmentFreshness(staleReason)
	}
	currentInstalledVersion = strings.TrimSpace(currentInstalledVersion)
	installedVersionWhenAssessed := strings.TrimSpace(assessment.InstalledVersion)
	if !storeAssessmentVersionKnown(currentInstalledVersion) {
		return stalePublishedStoreAssessmentFreshness("current installed version is unavailable")
	}
	if !storeAssessmentVersionKnown(installedVersionWhenAssessed) {
		return stalePublishedStoreAssessmentFreshness("assessed installed version is unavailable")
	}
	if !strings.EqualFold(currentInstalledVersion, installedVersionWhenAssessed) {
		return stalePublishedStoreAssessmentFreshness("installed version no longer matches the assessed version")
	}
	return storePublishedAssessmentFreshness{Fresh: true}
}

func storeAssessmentVersionKnown(version string) bool {
	version = strings.TrimSpace(version)
	return version != "" && !strings.EqualFold(version, "unknown")
}

func staleStoreEvidenceTimeReason(evidenceLabel string, evidenceTime time.Time, now time.Time) string {
	if evidenceTime.IsZero() {
		return fmt.Sprintf("%s time is unavailable", evidenceLabel)
	}
	evidenceAge := now.Sub(evidenceTime.UTC())
	if evidenceAge < 0 {
		evidenceAge = 0
	}
	if evidenceAge > storeAssessmentFreshnessWindow {
		return fmt.Sprintf("%s evidence is older than %s", evidenceLabel, storeAssessmentFreshnessWindow)
	}
	return ""
}

func stalePublishedStoreAssessmentFreshness(reason string) storePublishedAssessmentFreshness {
	return storePublishedAssessmentFreshness{Reason: sanitizeProviderDiagnostic(reason)}
}

func staleStoreAssessmentProjection(assessment StorePublishedAssessment, reason string) StorePublishedAssessment {
	assessment.Stale = true
	assessment.ExactActionTargetAvailable = false
	if reason != "" {
		existingReason := firstNonEmpty(assessment.Reason, "stale Store update evidence")
		assessment.Reason = sanitizeProviderDiagnostic(existingReason + ": " + reason)
	}
	return assessment
}
