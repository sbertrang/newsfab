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


func main() {
	var configFile = "newsfab.yaml"
	var outputFile = ""
	var templateFile = "html.tmpl"

	flag.StringVar( &configFile, "c", configFile, "config file" )
	flag.StringVar( &outputFile, "o", outputFile, "output file" )
	flag.StringVar( &templateFile, "t", templateFile, "template file" )
	flag.Parse()

	cfg, err := config.ParseYamlFile( configFile )
	if err != nil {
		log.Fatal( err )
	}

	curls, err := cfg.List( "urls" )
	if err != nil {
		log.Fatal( err )
	}

	urls := make( []string, 0 )
	for _, curl := range curls {
		if url, ok := curl.( string ); ok {
			urls = append( urls, url )
		} else {
			log.Printf( "Skipping invalid URL: %s\n", curl )
		}
	}

	tmpl, err := template.New( templateFile ).ParseFiles( templateFile )
	if err != nil {
		log.Fatal( err )
	}

	feeds := make( []*gofeed.Feed, 0 )

	cache := diskcache.New( ".cache" )
	transport := httpcache.NewTransport( cache )
	client := http.Client{ Transport: transport }
	fp := gofeed.NewParser()
	fp.Client = &client

	ctx, cancel := context.WithTimeout( context.Background(), 10 * time.Second )
	defer cancel()
	var wg sync.WaitGroup
	for _, url := range urls {
		wg.Add( 1 )
		go func( url string ) {
			feed, err := fp.ParseURLWithContext( url, ctx )
			if err != nil {
				log.Fatal( err )
			}
			feeds = append( feeds, feed )
			wg.Done()
		}( url )
	}
	wg.Wait()

	data := struct {
		Feeds   []*gofeed.Feed
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

