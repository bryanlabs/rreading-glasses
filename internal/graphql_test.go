package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blampe/rreading-glasses/gr"
	"github.com/blampe/rreading-glasses/hardcover"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestQueryBuilderMultipleQueries(t *testing.T) {
	t.Run("hardcover", func(t *testing.T) {
		qb := newQueryBuilder()

		query1 := hardcover.GetWork_Operation
		vars1 := map[string]any{"grBookIDs": []string{"1"}}

		query2 := hardcover.GetAuthorEditions_Operation
		vars2 := map[string]any{
			"id":     1,
			"limit":  2,
			"offset": 3,
		}

		id1, _, err := qb.add(query1, vars1)
		require.NoError(t, err)

		id2, _, err := qb.add(query2, vars2)
		require.NoError(t, err)

		query, vars, err := qb.build()
		require.NoError(t, err)

		expected := fmt.Sprintf(`query GetWork($%s_bookID: Int!, $%s_id: Int!, $%s_limit: Int!, $%s_offset: Int!) {
  %s: books_by_pk(id: $%s_bookID) {
    ...WorkInfo
    editions(order_by: {score: desc_nulls_last}) {
      ...EditionInfo
    }
  }
  %s: authors_by_pk(id: $%s_id) {
    ...AuthorInfo
    contributions(limit: $%s_limit, offset: $%s_offset, order_by: {book: {ratings_count: desc}}, where: {contributable_type: {_eq: "Book"}, book: {book_status_id: {_eq: "1"}}}) {
      ...Contributions
      book {
        id
        title
        ratings_count
        ...DefaultEditions
      }
    }
  }
}
fragment AuthorInfo on authors {
  id
  name
  slug
  bio
  cached_image(path: "url")
}
fragment Contributions on contributions {
  contribution
  author {
    ...AuthorInfo
  }
}
fragment DefaultEditions on books {
  id
  contributions {
    ...Contributions
  }
  default_audio_edition {
    id
    contributions {
      ...Contributions
    }
  }
  default_physical_edition {
    id
    contributions {
      ...Contributions
    }
  }
  default_cover_edition {
    id
    contributions {
      ...Contributions
    }
  }
  default_ebook_edition {
    id
    contributions {
      ...Contributions
    }
  }
  fallback: editions(order_by: {id: desc}, limit: 1) {
    id
  }
}
fragment EditionInfo on editions {
  id
  title
  subtitle
  asin
  isbn_13
  edition_format
  pages
  audio_seconds
  language {
    code3
  }
  publisher {
    name
  }
  release_date
  audio_seconds
  physical_format
  physical_information
  edition_information
  users_read_count
  book_id
  score
}
fragment WorkInfo on books {
  id
  title
  subtitle
  description
  release_date
  cached_tags(path: "$.Genre")
  cached_image(path: "url")
  slug
  state
  canonical_id
  book_series {
    position
    series {
      id
      name
      description
    }
  }
  rating
  ratings_count
  ...DefaultEditions
}`, id1, id2, id2, id2, id1, id1, id2, id2, id2, id2)

		assert.Equal(t, expected, query, query)

		assert.Len(t, vars, 4)
		assert.Contains(t, vars, id1+"_bookID", id2+"_id", id2+"_limit", id2+"_offset")
	})

	t.Run("gr", func(t *testing.T) {
		qb := newQueryBuilder()

		query1 := gr.GetBook_Operation
		vars1 := map[string]any{"legacyId": []string{"1"}}

		query2 := gr.GetAuthorWorks_Operation
		vars2 := map[string]any{
			"pagination":                 map[string]string{},
			"getWorksByContributorInput": map[string]string{},
		}

		id1, _, err := qb.add(query1, vars1)
		require.NoError(t, err)

		id2, _, err := qb.add(query2, vars2)
		require.NoError(t, err)

		query, vars, err := qb.build()
		require.NoError(t, err)

		expected := fmt.Sprintf(`query GetBook($%s_legacyId: Int!, $%s_getWorksByContributorInput: GetWorksByContributorInput!, $%s_pagination: PaginationInput!) {
  %s: getBookByLegacyId(legacyId: $%s_legacyId) {
    ...BookInfo
    work {
      id
      legacyId
      details {
        webUrl
        publicationTime
      }
      bestBook {
        legacyId
        title
        titlePrimary
        primaryContributorEdge {
          role
          node {
            legacyId
          }
        }
      }
      editions {
        edges {
          node {
            ...BookInfo
          }
        }
      }
    }
  }
  %s: getWorksByContributor(getWorksByContributorInput: $%s_getWorksByContributorInput, pagination: $%s_pagination) {
    edges {
      node {
        id
        bestBook {
          legacyId
          primaryContributorEdge {
            role
            node {
              legacyId
            }
          }
          secondaryContributorEdges {
            role
          }
        }
      }
    }
    pageInfo {
      hasNextPage
      nextPageToken
    }
  }
}
fragment BookInfo on Book {
  id
  legacyId
  description(stripped: true)
  bookGenres {
    genre {
      name
    }
  }
  bookSeries {
    series {
      id
      title
      webUrl
    }
    seriesPlacement
  }
  details {
    asin
    isbn13
    format
    numPages
    language {
      name
    }
    officialUrl
    publisher
    publicationTime
  }
  imageUrl
  primaryContributorEdge {
    node {
      id
      name
      legacyId
      webUrl
      profileImageUrl
      description
    }
  }
  stats {
    averageRating
    ratingsCount
    ratingsSum
  }
  title
  titlePrimary
  webUrl
}`, id1, id2, id2, id1, id1, id2, id2, id2)

		assert.Equal(t, expected, query)

		assert.Len(t, vars, 3)
		assert.Contains(t, vars, id1+"_legacyId", id2+"_getWorksByContributorInput", id2+"_pagination")
	})
}

func TestBatching(t *testing.T) {
	apiKey := os.Getenv("HARDCOVER_API_KEY")
	if apiKey == "" {
		t.Skip("missing HARDCOVER_API_KEY")
		return
	}
	transport := &HeaderTransport{
		Key:          "Authorization",
		Value:        "Bearer " + apiKey,
		RoundTripper: http.DefaultTransport,
	}

	client := &http.Client{Transport: transport}

	url := "https://api.hardcover.app/v1/graphql"

	gql, err := NewBatchedGraphQLClient(url, client, time.Second, 6, nil)
	require.NoError(t, err)

	start := time.Now()

	wg := sync.WaitGroup{}
	wg.Go(func() {
		_, err := hardcover.GetWork(context.Background(), gql, 156028352)
		if err != nil {
			panic(err)
		}
	})

	wg.Go(func() {
		_, err := hardcover.GetWork(context.Background(), gql, 164005178)
		if err != nil {
			panic(err)
		}
	})

	wg.Go(func() {
		_, err := hardcover.GetWork(context.Background(), gql, 340640138)
		if err != nil {
			panic(err)
		}
	})

	wg.Go(func() {
		_, err := hardcover.GetWork(context.Background(), gql, -1) // Missing.
		if err != nil {
			panic(err)
		}
	})

	wg.Wait()

	assert.Less(t, time.Since(start), 4*time.Second)
}

func TestBatchingOverflow(t *testing.T) {
	calls := atomic.Int32{}

	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			body := `{"data": {}, "errors": []}`
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}

	gql, err := NewBatchedGraphQLClient("https://foo.com", client, 50*time.Millisecond, 1, nil)
	require.NoError(t, err)

	wg := sync.WaitGroup{}

	// var resp1, resp2 *gr.GetBookResponse
	var err1, err2 error

	// Spawn more queries than our batch allows. They should get executed in
	// separate batches.
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err1 = gr.GetBook(t.Context(), gql, 1)
	}()
	go func() {
		defer wg.Done()
		_, err2 = gr.GetBook(t.Context(), gql, 2)
	}()
	wg.Wait()

	assert.NoError(t, err1)
	assert.NoError(t, err2)

	assert.Equal(t, int32(2), calls.Load())
}

func TestPacedRetriesRateLimit(t *testing.T) {
	_rateLimitPause = 100 * time.Millisecond
	t.Cleanup(func() { _rateLimitPause = 12 * time.Second })

	calls := atomic.Int32{}
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if calls.Add(1) <= 2 {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader(`{"data":null}`)),
				}, nil
			}
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"data": {}, "errors": []}`)),
			}, nil
		}),
	}

	gql, err := NewPacedGraphQLClient("https://foo.com", client, 20*time.Millisecond, 5, nil)
	require.NoError(t, err)

	_, err = gr.GetBook(t.Context(), gql, 1)
	assert.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load())
}

func TestPacedGivesUpOnRateLimit(t *testing.T) {
	_rateLimitPause = 10 * time.Millisecond
	t.Cleanup(func() { _rateLimitPause = 12 * time.Second })

	calls := atomic.Int32{}
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Body:       io.NopCloser(strings.NewReader(`{"data":null}`)),
			}, nil
		}),
	}

	gql, err := NewPacedGraphQLClient("https://foo.com", client, 10*time.Millisecond, 5, nil)
	require.NoError(t, err)

	_, err = gr.GetBook(t.Context(), gql, 1)
	assert.Error(t, err)
	assert.Equal(t, int32(_maxRateLimitRetries+1), calls.Load())
}

func TestPacedSpacesByQueryCount(t *testing.T) {
	type send struct {
		at      time.Time
		queries int
	}
	var mu sync.Mutex
	var sent []send
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			var body struct {
				Variables map[string]any `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			sent = append(sent, send{time.Now(), len(body.Variables)})
			mu.Unlock()
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"data": {}, "errors": []}`)),
			}, nil
		}),
	}

	every := 50 * time.Millisecond
	gql, err := NewPacedGraphQLClient("https://foo.com", client, every, 3, nil)
	require.NoError(t, err)

	wg := sync.WaitGroup{}
	for i := range 6 {
		wg.Go(func() { _, _ = gr.GetBook(t.Context(), gql, int64(i+1)) })
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	total := 0
	for i, s := range sent {
		total += s.queries
		if i == 0 {
			continue
		}
		prev := sent[i-1]
		assert.GreaterOrEqual(t, s.at.Sub(prev.at), time.Duration(max(prev.queries, 1))*every-5*time.Millisecond)
	}
	assert.Equal(t, 6, total)
}

func TestPacedServesForegroundFirst(t *testing.T) {
	var mu sync.Mutex
	var order []string
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			var body struct {
				Variables map[string]any `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			for _, v := range body.Variables {
				order = append(order, fmt.Sprint(v))
			}
			mu.Unlock()
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"data": {}, "errors": []}`)),
			}, nil
		}),
	}

	gql, err := NewPacedGraphQLClient("https://foo.com", client, 30*time.Millisecond, 1, nil)
	require.NoError(t, err)

	bg := context.WithValue(t.Context(), middleware.RequestIDKey, "refresh-author-1")
	fg := context.WithValue(t.Context(), middleware.RequestIDKey, "host/abc-000001")

	wg := sync.WaitGroup{}
	for i := range 4 {
		wg.Go(func() { _, _ = gr.GetBook(bg, gql, int64(100+i)) })
	}
	time.Sleep(10 * time.Millisecond)
	wg.Go(func() { _, _ = gr.GetBook(fg, gql, 1) })
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, order, 5)
	assert.Contains(t, order[:2], "1", "the waiting caller's query should jump the background backlog")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func TestGQLStatusCode(t *testing.T) {
	err := &gqlerror.Error{Message: "womp"}
	assert.ErrorIs(t, err, gqlStatusErr(err))

	err = &gqlerror.Error{Message: "Request failed with status code 403"}
	err403 := statusErr(403)
	assert.ErrorAs(t, gqlStatusErr(err), &err403)
}
