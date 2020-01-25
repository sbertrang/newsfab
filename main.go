package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"html/template"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/gregjones/httpcache"
	"github.com/gregjones/httpcache/diskcache"
	"github.com/mmcdole/gofeed"
	"github.com/olebedev/config"
	"github.com/sbertrang/atomic"
)

// HTTPError represents an HTTP error returned by a server.
type HTTPError struct {
	StatusCode int
	Status     string
}

func (err HTTPError) Error() string {
	return fmt.Sprintf("http error: %s", err.Status)
}

type Record struct {
	Feed	*gofeed.Feed
	Item	*gofeed.Item
	Time	*time.Time
}

type Feeds []*gofeed.Feed

func load_urls( file string ) ( []string, error ) {
	cfg, err := config.ParseYamlFile( file )
	if err != nil {
		return nil, err
	}

	curls, err := cfg.List( "urls" )
	if err != nil {
		return nil, err
	}

	urls := make( []string, 0 )
	for _, curl := range curls {
		if url, ok := curl.( string ); ok {
			urls = append( urls, url )
		} else {
			log.Printf( "Skipping invalid URL: %s\n", curl )
		}
	}

	return urls, nil
}

func fetch_feeds( urls []string, ctx context.Context ) ( Feeds, error ) {
	cache := diskcache.New( ".cache" )
	transport := httpcache.NewTransport( cache )
	client := http.Client{ Transport: transport }
	fp := gofeed.NewParser()
	fp.Client = &client

	feeds := make( Feeds, 0 )

	var wg sync.WaitGroup
	for _, url := range urls {
		wg.Add( 1 )
		go func( url string ) {
			feed, err := fp.ParseURLWithContext( url, ctx )
			if err != nil {
				log.Println( err )
			} else {
				feeds = append( feeds, feed )
			}
			wg.Done()
		}( url )
	}
	wg.Wait()

	return feeds, nil
}

func main() {
	var configFile = "newsfab.yaml"
	var outputFile = ""
	var templateFile = "html.tmpl"

	flag.StringVar( &configFile, "c", configFile, "config file" )
	flag.StringVar( &outputFile, "o", outputFile, "output file" )
	flag.StringVar( &templateFile, "t", templateFile, "template file" )
	flag.Parse()

	urls, err := load_urls( configFile )
	if err != nil {
		log.Fatal( err )
	}

	ctx, cancel := context.WithTimeout( context.Background(), 10 * time.Second )
	defer cancel()

	feeds, err := fetch_feeds( urls, ctx )
	if err != nil {
		log.Fatal( err )
	}

	data := struct {
		Feeds   Feeds
		Records	[]Record
	}{}

	data.Feeds = feeds
	data.Records = make( []Record, 0 )

	for _, feed := range feeds {
		for _, item := range feed.Items {
			t := item.UpdatedParsed
			if t == nil {
				t = item.PublishedParsed
			}
			data.Records = append( data.Records, Record{ feed, item, t } )
		}
	}

	sort.Slice( data.Feeds, func( i, j int ) bool {
		return data.Feeds[ i ].Title < data.Feeds[ j ].Title
	} )
	sort.Slice( data.Records, func( i, j int ) bool {
		return data.Records[ i ].Time.After( *data.Records[ j ].Time )
	} )

	tmpl, err := template.New( templateFile ).ParseFiles( templateFile )
	if err != nil {
		log.Fatal( err )
	}

	if outputFile == "" {
		if err := tmpl.Execute( os.Stdout, data ); err != nil {
			log.Fatal( err )
		}
	} else {
		r, w := io.Pipe()
		go func() {
			if err := tmpl.Execute( w, data ); err != nil {
				log.Fatal( err )
			}
			w.Close()
		}()
		log.Printf( "Writing output file: %s\n", outputFile )
		if err := atomic.WriteFile( outputFile, r ); err != nil {
			log.Fatal( err )
		}
	}
}

