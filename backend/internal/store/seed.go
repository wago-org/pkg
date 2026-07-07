package store

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/wago-org/registry-backend/internal/model"
)

// seedFile is the wago-registry/v1 seed document at PACKAGES_FILE.
type seedFile struct {
	Schema    string     `json:"schema"`
	SeedUsers []seedUser `json:"seedUsers"`
	Packages  []seedPkg  `json:"packages"`
}

type seedUser struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

// seedPkg embeds a model.Package (so every standard field unmarshals in place)
// and adds the seed-only review/comment fields consumed here but not stored on
// Package.
type seedPkg struct {
	model.Package
	SeedReviews  []seedReview  `json:"seedReviews"`
	SeedComments []seedComment `json:"seedComments"`
}

type seedReview struct {
	Login     string `json:"login"`
	Rating    int    `json:"rating"`
	CreatedAt string `json:"createdAt"`
	Body      string `json:"body"`
	Score     int    `json:"score"`
}

type seedComment struct {
	Login       string `json:"login"`
	CreatedAt   string `json:"createdAt"`
	Body        string `json:"body"`
	ParentIndex *int   `json:"parentIndex"`
}

// seedUserID is the stable id given to a seed user.
func seedUserID(login string) string { return "seed:" + login }

// Seed imports the wago-registry/v1 file at path into an empty store. It is a
// no-op when the store already has packages.
func Seed(s *JSONStore, path string) error {
	if s.PackageCount() > 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read seed file: %w", err)
	}
	var sf seedFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return fmt.Errorf("parse seed file: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()

	// Seed users get stable ids and empty avatars.
	for _, u := range sf.SeedUsers {
		id := seedUserID(u.Login)
		s.doc.Users[id] = model.User{ID: id, Login: u.Login, Name: u.Name}
	}

	for _, sp := range sf.Packages {
		p := sp.Package
		latest := p.LatestVersion()
		if p.UpdatedAt == "" {
			p.UpdatedAt = latest.PublishedAt
		}
		if p.CreatedAt == "" && len(p.Versions) > 0 {
			p.CreatedAt = p.Versions[len(p.Versions)-1].PublishedAt
		}
		s.doc.Packages[p.Short] = p

		// Reviews: each seed review becomes a Review by its seed user, with
		// `score` synthetic upvotes so the vote tally reproduces the seed score.
		for ri, sr := range sp.SeedReviews {
			rid := newID()
			s.doc.Reviews[rid] = model.Review{
				ID:           rid,
				PackageShort: p.Short,
				UserID:       seedUserID(sr.Login),
				Rating:       sr.Rating,
				Body:         sr.Body,
				CreatedAt:    sr.CreatedAt,
			}
			if sr.Score > 0 {
				votes := make(map[string]string, sr.Score)
				for n := 0; n < sr.Score; n++ {
					votes[fmt.Sprintf("seedvote:%s:%d:%d", p.Short, ri, n)] = "up"
				}
				s.doc.Votes[rid] = votes
			}
		}

		// Comments: created in order so parentIndex can resolve to an earlier id.
		ids := make([]string, len(sp.SeedComments))
		for ci, sc := range sp.SeedComments {
			cid := newID()
			ids[ci] = cid
			parent := ""
			if sc.ParentIndex != nil && *sc.ParentIndex >= 0 && *sc.ParentIndex < ci {
				parent = ids[*sc.ParentIndex]
			}
			s.doc.Comments[cid] = model.Comment{
				ID:           cid,
				PackageShort: p.Short,
				UserID:       seedUserID(sc.Login),
				Body:         sc.Body,
				CreatedAt:    sc.CreatedAt,
				ParentID:     parent,
			}
		}

		// Install history: 90 daily buckets around installBaseWeek/7 with a
		// deterministic ripple that sums to zero over any 7-day window, so
		// InstallWeek stays ~= installBaseWeek.
		base := p.InstallBaseWeek / 7
		if base > 0 {
			buckets := make(map[string]int, 90)
			for d := 0; d < 90; d++ {
				date := now.AddDate(0, 0, -d).Format("2006-01-02")
				ripple := (d%7 - 3) * (base / 20)
				count := base + ripple
				if count < 0 {
					count = 0
				}
				buckets[date] = count
			}
			s.doc.Installs[p.Short] = buckets
		}
	}

	return s.persistLocked()
}
