// Package ga provides functionality to parse General Availability (GA) data from Excel files for release tracking.
package ga

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shay23bra/pr-bot/internal/embedded"
	"github.com/shay23bra/pr-bot/internal/logger"
	"github.com/shay23bra/pr-bot/internal/models"
	"github.com/xuri/excelize/v2"
)

// Constants for product names and Excel sheet names.
const (
	ProductACM = "ACM"
	ProductMCE = "MCE"

	SheetInProgress = "In Progress"
	SheetCompleted  = "ACM MCE Completed "

	// Status constants
	StatusGA          = "GA"
	StatusNextVersion = "Next Version"
	StatusNotFound    = "Not Found"
	StatusMergedNotGA = "Merged but not GA"

	// Version mapping constants
	MCEVersionOffset = 5 // MCE version is 5 versions behind ACM
)

// Parser handles GA status parsing from Excel files.
type Parser struct {
	filePath string

	// Background parsing and caching
	cache        *parsedData
	cacheMutex   sync.RWMutex
	parseOnce    sync.Once
	parseChannel chan struct{} // Signals when parsing is complete
	parseError   error
}

// parsedData holds the cached Excel data
type parsedData struct {
	inProgressReleases []ReleaseInfo
	completedReleases  []ReleaseInfo
	allReleases        []ReleaseInfo
	lastParsed         time.Time
}

// NewParser creates a new GA parser and starts background Excel parsing.
func NewParser(filePath string) *Parser {
	p := &Parser{
		filePath:     filePath,
		parseChannel: make(chan struct{}),
	}

	// Start background parsing
	go p.backgroundParse()

	return p
}

// backgroundParse parses the Excel file in the background and caches the results.
func (p *Parser) backgroundParse() {
	p.parseOnce.Do(func() {
		logger.Debug("Starting background Excel file parsing")
		start := time.Now()

		var f *excelize.File
		var err error
		var cleanup func()

		// Try embedded data first
		if embedded.HasEmbeddedData() {
			logger.Debug("Using embedded Excel data (%d bytes)", embedded.GetDataSize())

			// Create temporary file from embedded data
			tempPath, cleanupFunc, tempErr := embedded.SaveEmbeddedDataToTempFile()
			if tempErr != nil {
				p.parseError = fmt.Errorf("failed to create temp file from embedded data: %w", tempErr)
				close(p.parseChannel)
				return
			}
			cleanup = cleanupFunc

			f, err = excelize.OpenFile(tempPath)
		} else if p.filePath != "" {
			// Fallback to filesystem
			logger.Debug("Using filesystem Excel file: %s", p.filePath)
			f, err = excelize.OpenFile(p.filePath)
		} else {
			p.parseError = fmt.Errorf("no Excel data available: not embedded and no file path provided")
			close(p.parseChannel)
			return
		}

		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			p.parseError = fmt.Errorf("failed to open Excel file: %w", err)
			close(p.parseChannel)
			return
		}

		// Ensure cleanup happens after parsing
		defer func() {
			f.Close()
			if cleanup != nil {
				cleanup()
			}
		}()

		// Parse both tabs
		inProgressReleases, inProgressErr := p.parseInProgressTab(f)
		if inProgressErr != nil {
			logger.Debug("Warning: failed to parse 'In Progress' tab: %v", inProgressErr)
		}

		completedReleases, completedErr := p.parseCompletedTab(f)
		if completedErr != nil {
			logger.Debug("Warning: failed to parse 'ACM MCE Completed ' tab: %v", completedErr)
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
		logger.Debug("Background Excel parsing completed in %v (found %d total releases)",
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

// ReleaseInfo represents release information from Excel.
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

	logger.Debug("Using cached Excel data (parsed at %s, %d total releases)",
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

// parseInProgressTab parses the "In Progress" tab.
func (p *Parser) parseInProgressTab(f *excelize.File) ([]ReleaseInfo, error) {
	var releases []ReleaseInfo

	// Get rows from "In Progress" sheet
	rows, err := f.GetRows(SheetInProgress)
	if err != nil {
		return nil, fmt.Errorf("failed to get rows from '%s' tab: %w", SheetInProgress, err)
	}

	logger.Debug("Processing %d rows in '%s' tab", len(rows), SheetInProgress)

	// Look for rows with version information
	for i, row := range rows {
		if len(row) < 2 {
			continue
		}

		// Extract version information from this row
		acmVersion := p.extractVersion(row, ProductACM)
		mceVersion := p.extractVersion(row, ProductMCE)

		// If we found version information, look for dates in the same row
		if acmVersion != "" || mceVersion != "" {
			logger.Debug("Found version row %d: ACM=%s, MCE=%s", i+1, acmVersion, mceVersion)

			// Find all dates in this row and take the latest one
			latestDate := p.findLatestDateInRow(row)

			if latestDate != nil {
				logger.Debug("Found GA date in row %d: %s", i+1, models.FormatDateWithNil(latestDate))
			} else {
				logger.Debug("No dates found in row %d", i+1)
			}

			release := ReleaseInfo{
				ACMVersion: acmVersion,
				MCEVersion: mceVersion,
				GADate:     latestDate,
				IsGA:       latestDate != nil && latestDate.Before(time.Now()),
			}
			releases = append(releases, release)
			logger.Debug("Added release: ACM %s, MCE %s, GA: %s", acmVersion, mceVersion, models.FormatDateWithNil(latestDate))
		}
	}

	return releases, nil
}

// findLatestDateInRow finds all dates in a row and returns the latest one.
func (p *Parser) findLatestDateInRow(row []string) *time.Time {
	var latestDate *time.Time

	logger.Debug("Scanning row for dates: %v", row)

	for i, cell := range row {
		if date := p.parseDateFromText(cell); date != nil {
			logger.Debug("Found date in column %d: %s -> %s", i+1, cell, models.FormatDateWithNil(date))

			if latestDate == nil || date.After(*latestDate) {
				latestDate = date
				logger.Debug("Updated latest date to: %s", models.FormatDateWithNil(latestDate))
			}
		}
	}

	if latestDate != nil {
		logger.Debug("Latest date in row: %s", models.FormatDateWithNil(latestDate))
	} else {
		logger.Debug("No dates found in row")
	}

	return latestDate
}

// parseCompletedTab parses the "ACM MCE Completed " tab.
func (p *Parser) parseCompletedTab(f *excelize.File) ([]ReleaseInfo, error) {
	var releases []ReleaseInfo

	// Get rows from "ACM MCE Completed " sheet (note the trailing space)
	rows, err := f.GetRows(SheetCompleted)
	if err != nil {
		return nil, fmt.Errorf("failed to get rows from '%s' tab: %w", SheetCompleted, err)
	}

	logger.Debug("Processing %d rows in '%s' tab", len(rows), SheetCompleted)

	// Parse each row looking for version and date pairs
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}

		// Look for ACM/MCE version patterns
		acmVersion := p.extractVersionFromText(row[0], ProductACM)
		mceVersion := p.extractVersionFromText(row[0], ProductMCE)

		if acmVersion != "" || mceVersion != "" {
			gaDate := p.parseDateFromText(row[1])

			release := ReleaseInfo{
				ACMVersion: acmVersion,
				MCEVersion: mceVersion,
				GADate:     gaDate,
				IsGA:       true, // Completed tab means it's GA
			}
			releases = append(releases, release)
			logger.Debug("Added completed release: ACM %s, MCE %s, GA: %s", acmVersion, mceVersion, models.FormatDateWithNil(gaDate))
		}
	}

	return releases, nil
}

// extractMajorMinor extracts the major.minor version from a full version string
func (p *Parser) extractMajorMinor(version string) string {
	// Extract major.minor from version like "2.14.1" -> "2.14"
	parts := strings.Split(version, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return version
}

// mapReleaseToProductVersion maps release branch version to expected product version
func (p *Parser) mapReleaseToProductVersion(releaseVersion, product string) string {
	if product == ProductACM {
		// ACM version matches release version (e.g., release-ocm-2.14 -> ACM 2.14.x)
		return releaseVersion
	} else if product == ProductMCE {
		// MCE version is 5 versions behind ACM (e.g., release-ocm-2.14 -> MCE 2.9.x)
		parts := strings.Split(releaseVersion, ".")
		if len(parts) >= 2 {
			major, err1 := strconv.Atoi(parts[0])
			minor, err2 := strconv.Atoi(parts[1])

			if err1 == nil && err2 == nil {
				// MCE minor version is offset behind ACM
				mceMinor := minor - MCEVersionOffset
				if mceMinor >= 0 {
					return fmt.Sprintf("%d.%d", major, mceMinor)
				}
			}
		}
	}

	return releaseVersion
}

// parseDateFromText parses date from text in various formats
func (p *Parser) parseDateFromText(text string) *time.Time {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// Try different date formats
	formats := []string{
		"1/2/2006",        // 3/25/2025
		"01/02/2006",      // 03/25/2025
		"2006-01-02",      // 2025-03-25
		"Jan 2, 2006",     // Mar 25, 2025
		"January 2, 2006", // March 25, 2025
	}

	for _, format := range formats {
		if date, err := time.Parse(format, text); err == nil {
			return &date
		}
	}

	// Try month/day format (like "5/14", "6/4")
	if matched, _ := regexp.MatchString(`^\d{1,2}/\d{1,2}$`, text); matched {
		currentYear := time.Now().Year()
		now := time.Now()

		// Try current year first
		if date, err := time.Parse("1/2/2006", text+"/"+fmt.Sprintf("%d", currentYear)); err == nil {
			// For dates in the current year, use a more intelligent logic:
			// - If the date is more than 6 months in the past, it's likely for next year
			// - If the date is within 6 months (past or future), it's likely for current year
			sixMonthsAgo := now.AddDate(0, -6, 0)
			
			if date.Before(sixMonthsAgo) {
				// Date is more than 6 months ago, assume next year
				if dateNextYear, err := time.Parse("1/2/2006", text+"/"+fmt.Sprintf("%d", currentYear+1)); err == nil {
					return &dateNextYear
				}
			}
			return &date
		}
	}

	return nil
}

// extractVersion extracts version for a specific product from a row
func (p *Parser) extractVersion(row []string, product string) string {
	for _, cell := range row {
		if version := p.extractVersionFromText(cell, product); version != "" {
			return version
		}
	}
	return ""
}

// extractVersionFromText extracts version from text for a specific product
func (p *Parser) extractVersionFromText(text, product string) string {
	// Look for patterns like "ACM 2.13.3" or "MCE 2.8.2"
	pattern := fmt.Sprintf(`%s\s+(\d+\.\d+\.\d+)`, product)
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(text)

	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// findLatestAndNextGA finds the latest and next GA for a specific product
func (p *Parser) findLatestAndNextGA(releases []ReleaseInfo, product string, expectedMajorMinor string, mergedAt *time.Time, now time.Time) (models.GAInfo, models.GAInfo) {
	var latestGA models.GAInfo
	var nextGA models.GAInfo
	var latestGADate *time.Time
	var nextGADate *time.Time

	logger.Debug("Looking for %s version %s", product, expectedMajorMinor)

	// Find matching versions for this product
	for _, release := range releases {
		var releaseVersion string
		var releaseGADate *time.Time

		if product == ProductACM && release.ACMVersion != "" {
			releaseVersion = release.ACMVersion
			releaseGADate = release.GADate
		} else if product == ProductMCE && release.MCEVersion != "" {
			releaseVersion = release.MCEVersion
			releaseGADate = release.GADate
		} else {
			continue
		}

		// Check if this version matches our expected major.minor
		releaseMajorMinor := p.extractMajorMinor(releaseVersion)
		logger.Debug("Comparing %s version %s with expected %s", product, releaseMajorMinor, expectedMajorMinor)

		if releaseMajorMinor == expectedMajorMinor && releaseGADate != nil {
			logger.Debug("Found match: %s %s with GA date %s", product, releaseVersion, models.FormatDateWithNil(releaseGADate))

			// Check if this is a past GA (latest) or future GA (next)
			if releaseGADate.Before(now) {
				// This is a past GA - check if it's the latest
				if latestGADate == nil || releaseGADate.After(*latestGADate) {
					latestGADate = releaseGADate
					latestGA = models.GAInfo{
						Version: releaseVersion,
						GADate:  releaseGADate,
						IsGA:    true,
						Status:  StatusGA,
					}
					logger.Debug("Updated latest %s GA to %s (GA: %s)", product, releaseVersion, models.FormatDateWithNil(releaseGADate))
				}
			} else {
				// This is a future GA - check if it's the next (earliest)
				if nextGADate == nil || releaseGADate.Before(*nextGADate) {
					nextGADate = releaseGADate
					status := StatusNextVersion
					if mergedAt != nil && mergedAt.Before(*releaseGADate) {
						status = StatusNextVersion
					}
					nextGA = models.GAInfo{
						Version:  releaseVersion,
						GADate:   releaseGADate,
						IsGA:     false,
						IsInNext: true,
						Status:   status,
					}
					logger.Debug("Updated next %s GA to %s (GA: %s)", product, releaseVersion, models.FormatDateWithNil(releaseGADate))
				}
			}
		}
	}

	// If no GA found, set status as "Not Found"
	if latestGA.Version == "" {
		latestGA = models.GAInfo{
			Version: expectedMajorMinor,
			Status:  StatusNotFound,
		}
		logger.Debug("No match found for latest %s version %s", product, expectedMajorMinor)
	}

	if nextGA.Version == "" {
		nextGA = models.GAInfo{
			Version: expectedMajorMinor,
			Status:  StatusNotFound,
		}
		logger.Debug("No match found for next %s version %s", product, expectedMajorMinor)
	}

	return latestGA, nextGA
}

// findPastAndFutureVersionsForProduct finds past and future GA versions for a specific product
func (p *Parser) findPastAndFutureVersionsForProduct(releases []ReleaseInfo, product string, expectedMajorMinor string, now time.Time) ([]models.UpcomingGA, []models.UpcomingGA) {
	var past []models.UpcomingGA
	var future []models.UpcomingGA

	for _, release := range releases {
		var releaseVersion string
		var releaseGADate *time.Time

		if product == ProductACM && release.ACMVersion != "" {
			releaseVersion = release.ACMVersion
			releaseGADate = release.GADate
		} else if product == ProductMCE && release.MCEVersion != "" {
			releaseVersion = release.MCEVersion
			releaseGADate = release.GADate
		} else {
			continue
		}

		// Check if this version matches our expected major.minor
		releaseMajorMinor := p.extractMajorMinor(releaseVersion)
		logger.Debug("Comparing %s version %s with expected %s", product, releaseMajorMinor, expectedMajorMinor)

		if releaseMajorMinor == expectedMajorMinor && releaseGADate != nil {
			logger.Debug("Found match: %s %s with GA date %s", product, releaseVersion, models.FormatDateWithNil(releaseGADate))

			// Check if this is a past GA (past) or future GA (future)
			if releaseGADate.Before(now) {
				past = append(past, models.UpcomingGA{
					Product: product,
					Version: releaseVersion,
					GADate:  releaseGADate,
				})
				logger.Debug("Added past %s GA: %s (%s)", product, releaseVersion, models.FormatDateWithNil(releaseGADate))
			} else {
				future = append(future, models.UpcomingGA{
					Product: product,
					Version: releaseVersion,
					GADate:  releaseGADate,
				})
				logger.Debug("Added future %s GA: %s (%s)", product, releaseVersion, models.FormatDateWithNil(releaseGADate))
			}
		}
	}

	return past, future
}

// findVersionsAfterMergeForProduct finds GA versions that occur after the merge date for a specific product
func (p *Parser) findVersionsAfterMergeForProduct(releases []ReleaseInfo, product string, expectedMajorMinor string, mergeDate time.Time) []models.UpcomingGA {
	var versionsAfterMerge []models.UpcomingGA

	for _, release := range releases {
		var releaseVersion string
		var releaseGADate *time.Time

		if product == ProductACM && release.ACMVersion != "" {
			releaseVersion = release.ACMVersion
			releaseGADate = release.GADate
		} else if product == ProductMCE && release.MCEVersion != "" {
			releaseVersion = release.MCEVersion
			releaseGADate = release.GADate
		} else {
			continue
		}

		// Check if this version matches our expected major.minor
		releaseMajorMinor := p.extractMajorMinor(releaseVersion)
		logger.Debug("Comparing %s version %s with expected %s", product, releaseMajorMinor, expectedMajorMinor)

		if releaseMajorMinor == expectedMajorMinor && releaseGADate != nil {
			logger.Debug("Found match: %s %s with GA date %s", product, releaseVersion, models.FormatDateWithNil(releaseGADate))

			// Only include GAs that occur after the merge date
			if releaseGADate.After(mergeDate) {
				versionsAfterMerge = append(versionsAfterMerge, models.UpcomingGA{
					Product: product,
					Version: releaseVersion,
					GADate:  releaseGADate,
				})
				logger.Debug("Added %s GA after merge: %s (%s)", product, releaseVersion, models.FormatDateWithNil(releaseGADate))
			} else {
				logger.Debug("Skipping %s GA before/at merge date: %s (%s)", product, releaseVersion, models.FormatDateWithNil(releaseGADate))
			}
		}
	}

	return versionsAfterMerge
}

// GetAllMCEReleases gets all MCE releases from the Excel file
func (p *Parser) GetAllMCEReleases() ([]ReleaseInfo, error) {
	logger.Debug("Getting all MCE releases from cached Excel data")

	// Wait for cached data
	data, err := p.waitForData()
	if err != nil {
		return nil, fmt.Errorf("failed to get cached data: %w", err)
	}

	var mceReleases []ReleaseInfo
	for _, release := range data.allReleases {
		if release.MCEVersion != "" && release.GADate != nil {
			mceReleases = append(mceReleases, release)
			logger.Debug("Found MCE release: %s (GA: %s)", release.MCEVersion, models.FormatDateWithNil(release.GADate))
		}
	}

	logger.Debug("Found %d MCE releases with versions and dates", len(mceReleases))
	return mceReleases, nil
}
