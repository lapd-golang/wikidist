package crawler

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/wikidistance/wikidist/pkg/db"
	"github.com/wikidistance/wikidist/pkg/metrics"
)

type httpGetter interface {
	Get(url string) (*http.Response, error)
}

// CrawlArticle : Crawls an article given its title
func CrawlArticle(title string, prefix string, hg httpGetter) (db.Article, error) {
	baseURL := "https://" + prefix + ".wikipedia.org/w/api.php"

	query := url.Values{}
	query.Set("format", "json")
	query.Add("action", "query")
	query.Add("prop", "links|description")
	query.Add("pllimit", "500")
	query.Add("plnamespace", "0")
	query.Add("titles", title)

	resp, err := hg.Get(baseURL + "?" + query.Encode())
	if err != nil {
		log.Printf("Request failed for article %s: %w", title, err)
		metrics.Statsd.Count("wikidist.requests", 1, []string{"state:hard_failure"}, 1)
		return db.Article{}, err
	}
	defer resp.Body.Close()
	metrics.Statsd.Count("wikidist.requests", 1, []string{"state:" + strconv.FormatInt(int64(resp.StatusCode), 10)}, 1)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		log.Println("Request failed for article", title, ", status", resp.StatusCode)
		return db.Article{}, fmt.Errorf("received non-2XX HTTP status code (possibly rate limited?)")
	}

	body, _ := ioutil.ReadAll(resp.Body)

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	missing, description, links, pageID, err := parseResponse(result)

	if err != nil {
		log.Println("Error while fetching article", title, ":", err)
		return db.Article{}, err
	}

	linkedArticles := make([]db.Article, 0, len(links))
	for _, link := range links {
		linkedArticles = append(linkedArticles, db.Article{
			Title: link,
		})
	}

	return db.Article{
		Title:          title,
		Description:    description,
		Missing:        missing,
		LinkedArticles: linkedArticles,
		PageID:         pageID,
	}, nil
}

func parseResponse(response map[string]interface{}) (bool, string, []string, int, error) {
	if _, ok := response["query"]; !ok {
		return false, "", []string{}, 0, fmt.Errorf("Malformed response")
	}
	query := response["query"].(map[string]interface{})

	if _, ok := query["pages"]; !ok {
		return false, "", []string{}, 0, fmt.Errorf("Malformed response")
	}
	titles := make([]string, 0)
	for _, value := range (query["pages"]).(map[string]interface{}) {
		page := value.(map[string]interface{})

		pageID := 0
		if id, ok := page["pageid"]; ok {
			pageID = int(id.(float64))
		}

		// handle when page is missing
		if _, ok := page["missing"]; ok {
			return true, "", []string{}, 0, nil
		}

		description := ""
		if desc, ok := page["description"]; ok {
			description = desc.(string)
		}

		if _, ok := page["links"]; !ok {
			return false, description, []string{}, 0, nil
		}

		links := page["links"].([]interface{})
		for _, value := range links {
			link := value.(map[string]interface{})
			switch link["title"].(type) {
			case string:
				titles = append(titles, link["title"].(string))
			default:
				return true, "", []string{}, 0, fmt.Errorf("Incorrect title in answer")
			}
		}

		return false, description, titles, pageID, nil
	}

	return true, "", []string{}, 0, fmt.Errorf(" No page in answer")
}
