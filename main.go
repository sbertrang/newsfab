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
	"os/signal"
	"sort"
	"sync"
	"syscall"
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

type News struct {
	Feeds   Feeds
	Records	[]Record
}

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
	feeds := make( Feeds, 0 )
	fp := gofeed.NewParser()
	fp.Client = &http.Client{
		Transport: httpcache.NewTransport( diskcache.New( ".cache" ) ),
	}

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

func prepare_news( feeds Feeds ) News {
	data := News{
		Feeds: feeds,
		Records: make( []Record, 0 ),
	}

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

	return data
}

func get_news( urls []string, templateFile string, outputFile string, ctx context.Context ) error {
	ctx, cancel := context.WithTimeout( ctx, 10 * time.Second )
	defer cancel()

	feeds, err := fetch_feeds( urls, ctx )
	if err != nil {
		return err
	}

	data := prepare_news( feeds )

	tmpl, err := template.New( templateFile ).ParseFiles( templateFile )
	if err != nil {
		return err
	}

	r, w := io.Pipe()
	go func() {
		if err := tmpl.Execute( w, data ); err != nil {
			log.Println( err )
		}
		w.Close()
	}()

	log.Printf( "Writing output file: %s\n", outputFile )

	if err := atomic.WriteFile( outputFile, r ); err != nil {
		return err
	}

	return nil
}

func main() {
	var configFile = "newsfab.yaml"
	var outputFile = "newsfab.html"
	var templateFile = "html.tmpl"

	flag.StringVar( &configFile, "c", configFile, "config file" )
	flag.StringVar( &outputFile, "o", outputFile, "output file" )
	flag.StringVar( &templateFile, "t", templateFile, "template file" )
	flag.Parse()

	urls, err := load_urls( configFile )
	if err != nil {
		log.Fatal( err )
	}

	ctx := context.Background()

	ctx, cancel := context.WithCancel( ctx )
	c := make( chan os.Signal, 1 )
	r := make( chan os.Signal, 1 )
	t := make( chan os.Signal, 1 )
	signal.Notify( c, syscall.SIGINT )
	signal.Notify( r, syscall.SIGHUP )
	signal.Notify( t, syscall.SIGTERM )

	// stop listening for signals and cancel context when leaving scope
	defer func() {
		signal.Stop( c )
		signal.Stop( r )
		signal.Stop( t )
		cancel()
	}()

	// periodic fetching of news
	ticker := time.NewTicker( 5 * time.Minute )
	stop := make( chan bool, 1 )
	go func() {
		for {
			if err := get_news( urls, templateFile, outputFile, ctx ); err != nil {
				log.Fatal( err )
			}
			select {
			case <- ticker.C:
			case <- stop:
				ticker.Stop()
				return
			}
		}
	}()

	// wait for signal to reload config
	go func() {
		for {
			select {
			case <- r:
				log.Println( "Reloading:", configFile )

				nurls, err := load_urls( configFile )
				if err != nil {
					log.Println( err )
				} else {
					urls = nurls
					log.Println( "New configuration:", urls )
				}
			}
		}
	}()

	// wait for final event
	select {
	case <- c:
		fmt.Println( "ancel..." )
		stop <- true
		cancel()
	case <- t:
		log.Println( "Terminate..." )
		stop <- true
		cancel()
	case <- ctx.Done():
		log.Println( "Done..." )
	}

	log.Println( "Exiting" )
}

