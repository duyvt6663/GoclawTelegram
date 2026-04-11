package jobcrawler

import "github.com/nextlevelbuilder/goclaw/internal/beta"

func init() {
	beta.Register(&JobCrawlerFeature{})
}
