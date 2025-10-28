// Package ga provides functionality to parse General Availability (GA) data from Google Sheets for release tracking.
package ga

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shay23bra/pr-bot/internal/logger"
	"github.com/shay23bra/pr-bot/internal/models"
)

// Constants for product names and sheet names.
const (
	ProductACM = "ACM"
	ProductMCE = "MCE"

	SheetInProgress = "In Progress"
	SheetCompleted  = "Completed Releases"

	// Status constants
	StatusGA          = "GA"
	StatusNextVersion = "Next Version"
	StatusNotFound    = "Not Found"
	StatusMergedNotGA = "Merged but not GA"

	// Version mapping constants
	MCEVersionOffset = 5 // MCE version is 5 versions behind ACM
)

// Parser handles GA status parsing from Google Sheets.
type Parser struct {
	sheetsClient *SheetsClient

	// Background parsing and caching
	cache        *parsedData
	cacheMutex   sync.RWMutex
	parseOnce    sync.Once
	parseChannel chan struct{} // Signals when parsing is complete
	parseError   error
}

// parsedData holds the cached Google Sheets data
type parsedData struct {
	inProgressReleases []ReleaseInfo
	completedReleases  []ReleaseInfo
	allReleases        []ReleaseInfo
	lastParsed         time.Time
}

// NewParser creates a new GA parser that uses Google Sheets API.
func NewParser(apiKey, sheetID string) (*Parser, error) {
	if apiKey == "" || sheetID == "" {
		return nil, fmt.Errorf("Google API key and Sheet ID are required")
	}

	sheetsClient, err := NewSheetsClient(apiKey, sheetID)
	if err != nil {
		return nil, fmt.Errorf("failed to create sheets client: %w", err)
	}

	p := &Parser{
		sheetsClient: sheetsClient,
		parseChannel: make(chan struct{}),
	}

	// Start background parsing
	go p.backgroundParse()

	return p, nil
}

// backgroundParse parses Google Sheets data in the background and caches the results.
func (p *Parser) backgroundParse() {
	p.parseOnce.Do(func() {
		start := time.Now()
		logger.Debug("Starting background Google Sheets parsing")

		// Read data from Google Sheets
		inProgressReleases, err := p.sheetsClient.ReadInProgressSheet()
		if err != nil {
			p.parseError = fmt.Errorf("failed to read 'In Progress' sheet: %w", err)
			close(p.parseChannel)
			return
		}

		completedReleases, err := p.sheetsClient.ReadCompletedSheet()
		if err != nil {
			p.parseError = fmt.Errorf("failed to read 'Completed Releases' sheet: %w", err)
			close(p.parseChannel)
			return
		}

		// Store in cache
		p.cacheMutex.Lock()
		p.cache = &parsedData{
			inProgressReleases: inProgressReleases,
			completedReleases:  completedReleases,
			allReleases:        append(inProgressReleases, completedReleases...),
			lastParsed:         time.Now(),
		}
		p.cacheMutex.Unlock()

		duration := time.Since(start)
		logger.Debug("Background Google Sheets parsing completed in %v (found %d total releases)",
			duration, len(p.cache.allReleases))

		// Signal that parsing is complete
		close(p.parseChannel)
	})
}

// waitForData waits for background parsing to complete and returns the cached data.
func (p *Parser) waitForData() (*parsedData, error) {
	// Wait for parsing to complete
	<-p.parseChannel

	if p.parseError != nil {
		return nil, p.parseError
	}

	p.cacheMutex.RLock()
	defer p.cacheMutex.RUnlock()

	if p.cache == nil {
		return nil, fmt.Errorf("parsed data not available")
	}

	return p.cache, nil
}

// ReleaseInfo represents release information from Google Sheets.
type ReleaseInfo struct {
	ACMVersion string
	MCEVersion string
	GADate     *time.Time
	IsGA       bool
}

// GetGAStatus gets GA status information for a specific version.
func (p *Parser) GetGAStatus(version string, mergedAt *time.Time) (models.GAStatus, error) {
	logger.Debug("Starting GA status analysis for version: %s", version)

	// Wait for cached data
	data, err := p.waitForData()
	if err != nil {
		return models.GAStatus{}, fmt.Errorf("failed to get cached data: %w", err)
	}

	logger.Debug("Using cached Google Sheets data (parsed at %s, %d total releases)",
		data.lastParsed.Format("15:04:05"), len(data.allReleases))

	// Convert version to expected product versions
	versionNum := strings.TrimPrefix(version, "release-ocm-")
	expectedACMVersion := p.mapReleaseToProductVersion(versionNum, ProductACM)
	expectedMCEVersion := p.mapReleaseToProductVersion(versionNum, ProductMCE)

	now := time.Now()

	// Find latest and next GA for ACM
	latestACM, nextACM := p.findLatestAndNextGA(data.allReleases, ProductACM, expectedACMVersion, mergedAt, now)

	// Find latest and next GA for MCE
	latestMCE, nextMCE := p.findLatestAndNextGA(data.allReleases, ProductMCE, expectedMCEVersion, mergedAt, now)

	return models.GAStatus{
		ACM:     latestACM,
		MCE:     latestMCE,
		NextACM: nextACM,
		NextMCE: nextMCE,
	}, nil
}

// GetUpcomingGAVersions finds the closest GA versions after the merge date.
func (p *Parser) GetUpcomingGAVersions(version string, mergedAt *time.Time) ([]models.UpcomingGA, error) {
	if mergedAt == nil {
		return nil, nil
	}

	logger.Debug("Finding closest GA versions for %s merged at %s", version, models.FormatDateWithNil(mergedAt))

	// Wait for cached data
	data, err := p.waitForData()
	if err != nil {
		return nil, fmt.Errorf("failed to get cached data: %w", err)
	}

	// Convert version to expected product versions
	versionNum := strings.TrimPrefix(version, "release-ocm-")
	expectedACMVersion := p.mapReleaseToProductVersion(versionNum, ProductACM)
	expectedMCEVersion := p.mapReleaseToProductVersion(versionNum, ProductMCE)

	logger.Debug("Looking for closest ACM %s.x and MCE %s.x versions after merge date %s", expectedACMVersion, expectedMCEVersion, models.FormatDateWithNil(mergedAt))

	var allGAsAfterMerge []models.UpcomingGA // All GAs after merge date

	// Find ACM versions after merge date
	acmVersionsAfterMerge := p.findVersionsAfterMergeForProduct(data.allReleases, ProductACM, expectedACMVersion, *mergedAt)
	allGAsAfterMerge = append(allGAsAfterMerge, acmVersionsAfterMerge...)

	// Find MCE versions after merge date
	mceVersionsAfterMerge := p.findVersionsAfterMergeForProduct(data.allReleases, ProductMCE, expectedMCEVersion, *mergedAt)
	allGAsAfterMerge = append(allGAsAfterMerge, mceVersionsAfterMerge...)

	// Sort all GAs after merge by date (earliest first)
	sort.Slice(allGAsAfterMerge, func(i, j int) bool {
		if allGAsAfterMerge[i].GADate == nil && allGAsAfterMerge[j].GADate == nil {
			return false
		}
		if allGAsAfterMerge[i].GADate == nil {
			return false
		}
		if allGAsAfterMerge[j].GADate == nil {
			return true
		}
		return allGAsAfterMerge[i].GADate.Before(*allGAsAfterMerge[j].GADate)
	})

	var result []models.UpcomingGA

	// Find the closest GA for each product (ACM and MCE)
	var closestACM, closestMCE *models.UpcomingGA

	for i, ga := range allGAsAfterMerge {
		if ga.Product == ProductACM && closestACM == nil {
			closestACM = &allGAsAfterMerge[i]
		} else if ga.Product == ProductMCE && closestMCE == nil {
			closestMCE = &allGAsAfterMerge[i]
		}

		// Break early if we found both
		if closestACM != nil && closestMCE != nil {
			break
		}
	}

	// Add the closest GAs to the result
	if closestACM != nil {
		result = append(result, *closestACM)
		logger.Debug("Added closest ACM GA after merge: %s %s (%s)", closestACM.Product, closestACM.Version, models.FormatDateWithNil(closestACM.GADate))
	}
	if closestMCE != nil {
		result = append(result, *closestMCE)
		logger.Debug("Added closest MCE GA after merge: %s %s (%s)", closestMCE.Product, closestMCE.Version, models.FormatDateWithNil(closestMCE.GADate))
	}

	logger.Debug("Found %d closest GA versions after merge date", len(result))
	return result, nil
}

// GetAllMCEReleases returns all releases from the cached data.
func (p *Parser) GetAllMCEReleases() ([]ReleaseInfo, error) {
	// Wait for cached data
	data, err := p.waitForData()
	if err != nil {
		return nil, fmt.Errorf("failed to get cached data: %w", err)
	}

	return data.allReleases, nil
}

// mapReleaseToProductVersion maps a release version to product version
func (p *Parser) mapReleaseToProductVersion(releaseVersion, product string) string {
	if product == ProductMCE {
		// MCE versions are offset by MCEVersionOffset
		if version, err := strconv.ParseFloat(releaseVersion, 64); err == nil {
			mceVersion := version - float64(MCEVersionOffset)/10.0
			return fmt.Sprintf("%.1f", mceVersion)
		}
	}
	return releaseVersion
}

// findLatestAndNextGA finds the latest and next GA for a specific product
func (p *Parser) findLatestAndNextGA(releases []ReleaseInfo, product, expectedVersion string, mergedAt *time.Time, now time.Time) (models.GAInfo, models.GAInfo) {
	var latest, next models.GAInfo

	for _, release := range releases {
		var version string
		if product == ProductACM {
			version = release.ACMVersion
		} else {
			version = release.MCEVersion
		}

		if version == "" {
			continue
		}

		// Check if this version matches our expected version pattern
		if !strings.HasPrefix(version, expectedVersion) {
			continue
		}

		gaInfo := models.GAInfo{
			Version: version,
			GADate:  release.GADate,
			Status:  StatusNotFound,
		}

		if release.GADate != nil {
			if release.GADate.Before(now) {
				gaInfo.Status = StatusGA
				gaInfo.IsGA = true
				if latest.Version == "" || p.compareVersions(version, latest.Version) > 0 {
					latest = gaInfo
				}
			} else {
				gaInfo.Status = StatusNextVersion
				gaInfo.IsInNext = true
				if next.Version == "" || p.compareVersions(version, next.Version) < 0 {
					next = gaInfo
				}
			}
		}
	}

	return latest, next
}

// findVersionsAfterMergeForProduct finds all versions for a product that have GA dates after the merge date
func (p *Parser) findVersionsAfterMergeForProduct(releases []ReleaseInfo, product, expectedVersion string, mergedAt time.Time) []models.UpcomingGA {
	var result []models.UpcomingGA

	for _, release := range releases {
		var version string
		if product == ProductACM {
			version = release.ACMVersion
		} else {
			version = release.MCEVersion
		}

		if version == "" || !strings.HasPrefix(version, expectedVersion) {
			continue
		}

		if release.GADate != nil && release.GADate.After(mergedAt) {
			upcomingGA := models.UpcomingGA{
				Product: product,
				Version: version,
				GADate:  release.GADate,
			}
			result = append(result, upcomingGA)
			logger.Debug("Found %s %s GA after merge: %s", product, version, models.FormatDateWithNil(release.GADate))
		}
	}

	return result
}

// compareVersions compares two version strings (e.g., "2.13.3" vs "2.13.4")
func (p *Parser) compareVersions(v1, v2 string) int {
	// Simple version comparison - split by dots and compare numerically
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var num1, num2 int

		if i < len(parts1) {
			if n, err := strconv.Atoi(parts1[i]); err == nil {
				num1 = n
			}
		}

		if i < len(parts2) {
			if n, err := strconv.Atoi(parts2[i]); err == nil {
				num2 = n
			}
		}

		if num1 < num2 {
			return -1
		} else if num1 > num2 {
			return 1
		}
	}

	return 0
}
