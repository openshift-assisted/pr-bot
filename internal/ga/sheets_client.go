// Package ga provides Google Sheets API integration for reading release schedule data.
package ga

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"

	"github.com/shay23bra/pr-bot/internal/logger"
	"github.com/shay23bra/pr-bot/internal/models"
)

// SheetsClient handles Google Sheets API operations
type SheetsClient struct {
	service *sheets.Service
	sheetID string
}

// NewSheetsClient creates a new Google Sheets client using service account authentication
func NewSheetsClient(serviceAccountJSON, sheetID string) (*SheetsClient, error) {
	ctx := context.Background()

	service, err := sheets.NewService(ctx, option.WithCredentialsJSON([]byte(serviceAccountJSON)))
	if err != nil {
		return nil, fmt.Errorf("failed to create sheets service with service account: %w", err)
	}

	return &SheetsClient{
		service: service,
		sheetID: sheetID,
	}, nil
}

// ReadInProgressSheet reads data from the "In Progress" sheet
func (c *SheetsClient) ReadInProgressSheet() ([]ReleaseInfo, error) {
	logger.Debug("Reading 'In Progress' sheet from Google Sheets")

	// Read the entire "In Progress" sheet
	readRange := "In Progress!A:Z"
	resp, err := c.service.Spreadsheets.Values.Get(c.sheetID, readRange).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to read In Progress sheet: %w", err)
	}

	var releases []ReleaseInfo

	logger.Debug("Processing %d rows from 'In Progress' sheet", len(resp.Values))

	for i, row := range resp.Values {
		if len(row) < 2 {
			continue
		}

		// Convert interface{} slice to string slice
		stringRow := make([]string, len(row))
		for j, cell := range row {
			if cell != nil {
				stringRow[j] = fmt.Sprintf("%v", cell)
			}
		}

		// Extract version information from this row
		acmVersion := c.extractVersion(stringRow, ProductACM)
		mceVersion := c.extractVersion(stringRow, ProductMCE)

		// If we found version information, look for dates in the same row
		if acmVersion != "" || mceVersion != "" {
			logger.Debug("Found version row %d: ACM=%s, MCE=%s", i+1, acmVersion, mceVersion)

			// Find all dates in this row and take the latest one
			latestDate := c.findLatestDateInRow(stringRow)

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

	logger.Debug("Parsed %d releases from 'In Progress' sheet", len(releases))
	return releases, nil
}

// ReadCompletedSheet reads data from the "Completed Releases" sheet
func (c *SheetsClient) ReadCompletedSheet() ([]ReleaseInfo, error) {
	logger.Debug("Reading 'Completed Releases' sheet from Google Sheets")

	// Read the entire "Completed Releases" sheet (note the trailing space)
	readRange := "Completed Releases!A:Z"
	resp, err := c.service.Spreadsheets.Values.Get(c.sheetID, readRange).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to read Completed Releasessheet: %w", err)
	}

	var releases []ReleaseInfo

	logger.Debug("Processing %d rows from 'Completed Releases' sheet", len(resp.Values))

	// Parse each row looking for version and date pairs
	for _, row := range resp.Values {
		if len(row) < 2 {
			continue
		}

		// Convert interface{} slice to string slice
		stringRow := make([]string, len(row))
		for j, cell := range row {
			if cell != nil {
				stringRow[j] = fmt.Sprintf("%v", cell)
			}
		}

		// Look for ACM/MCE version patterns
		acmVersion := c.extractVersionFromText(stringRow[0], ProductACM)
		mceVersion := c.extractVersionFromText(stringRow[0], ProductMCE)

		if acmVersion != "" || mceVersion != "" {
			gaDate := c.parseDateFromText(stringRow[1])

			release := ReleaseInfo{
				ACMVersion: acmVersion,
				MCEVersion: mceVersion,
				GADate:     gaDate,
				IsGA:       true, // Completed sheet means it's GA
			}
			releases = append(releases, release)
			logger.Debug("Added completed release: ACM %s, MCE %s, GA: %s", acmVersion, mceVersion, models.FormatDateWithNil(gaDate))
		}
	}

	logger.Debug("Parsed %d releases from 'Completed Releases' sheet", len(releases))
	return releases, nil
}

// extractVersion extracts version for a specific product from a row
func (c *SheetsClient) extractVersion(row []string, product string) string {
	for _, cell := range row {
		if version := c.extractVersionFromText(cell, product); version != "" {
			return version
		}
	}
	return ""
}

// extractVersionFromText extracts version from text for a specific product
func (c *SheetsClient) extractVersionFromText(text, product string) string {
	// Look for patterns like "ACM 2.13.3" or "MCE 2.8.2"
	pattern := fmt.Sprintf(`%s\s+(\d+\.\d+\.\d+)`, product)
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(text)

	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// findLatestDateInRow finds all dates in a row and returns the latest one
func (c *SheetsClient) findLatestDateInRow(row []string) *time.Time {
	var latestDate *time.Time

	logger.Debug("Scanning row for dates: %v", row)

	for i, cell := range row {
		if date := c.parseDateFromText(cell); date != nil {
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

// parseDateFromText parses date from text in various formats
func (c *SheetsClient) parseDateFromText(text string) *time.Time {
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
