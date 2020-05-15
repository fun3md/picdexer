package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/barasher/picdexer/conf"
	"github.com/barasher/picdexer/internal/common"
	"github.com/rs/zerolog/log"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	exif "github.com/barasher/go-exiftool"
)

const (
	apertureKey    = "Aperture"
	shutterKey     = "ShutterSpeed"
	keywordsKey    = "Keywords"
	cameraKey      = "Model"
	lensKey        = "LensModel"
	mimeTypeKey    = "MIMEType"
	heightKey      = "ImageHeight"
	widthKey       = "ImageWidth"
	captureDateKey = "CreateDate"
	gpsKey         = "GPSPosition"
	isoKey         = "ISO"

	srcDateFormat = "2006:01:02 15:04:05"

	ndJsonMimeType = "application/x-ndjson"
	bulkSuffix     = "_bulk"

	defaultExtrationThreadCount = 4
	defaultToExtractChannelSize = 50
)

type Indexer struct {
	conf        conf.ElasticsearchConf
	exif        *exif.Exiftool
}

type bulkEntryHeader struct {
	Index struct {
		Index string `json:"_index"`
		ID    string `json:"_id"`
	} `json:"index"`
}

func (idxer *Indexer) extractionThreadCount() int {
	n := idxer.conf.ExtractionThreadCount
	if n <1 {
		n = defaultExtrationThreadCount
	}
	return n
}

func (idxer *Indexer) toExtractChannelSize() int {
	n := idxer.conf.ToExtractChannelSize
	if n <1  {
		n = defaultToExtractChannelSize
	}
	return n
}

func NewIndexer(c conf.ElasticsearchConf) (*Indexer, error) {
	idxer := &Indexer{conf:c}

	et, err := exif.NewExiftool()
	if err != nil {
		return idxer, fmt.Errorf("error while initializing metadata extractor: %v", err)
	}
	idxer.exif = et

	return idxer, nil
}

func (idxer *Indexer) Close() error {
	if idxer.exif != nil {
		if err := idxer.exif.Close(); err != nil {
			log.Error().Msgf("error while closing exiftool: %v", err)
		}
	}
	return nil
}

type extractTask struct {
	path string
	info os.FileInfo
}

type printTask struct {
	header bulkEntryHeader
	pic    Model
}

func (idxer *Indexer) dump(ctx context.Context, cancel context.CancelFunc, globalWg *sync.WaitGroup, printChan chan printTask, writer io.Writer) {
	defer globalWg.Done()
	jsonEncoder := json.NewEncoder(writer)
	for task := range printChan {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := jsonEncoder.Encode(task.header); err != nil {
			log.Error().Msgf("error while encoding header: %v", err)
			cancel()
			return
		}
		if err := jsonEncoder.Encode(task.pic); err != nil {
			log.Error().Msgf("error while encoding json: %v", err)
			cancel()
			return
		}
	}
}

func (idxer *Indexer) startConverters(ctx context.Context, cancel context.CancelFunc, globalWg *sync.WaitGroup, toExtractChan chan extractTask, toDumpChan chan printTask) {
	defer globalWg.Done()
	threadCount := idxer.extractionThreadCount()
	var consumeWg sync.WaitGroup
	consumeWg.Add(threadCount)
	for i := 0; i < threadCount; i++ {
		go func(id int) {
			defer consumeWg.Done()
			for task := range toExtractChan {
				select {
				case <-ctx.Done():
					return
				default:
				}

				pic, err := idxer.convert(ctx, task.path, task.info)
				if err != nil {
					log.Error().Str(logFileIdentifier, task.path).Msgf("conversion error: %v", err)
					cancel()
					return
				} else {
					header, err := getBulkEntryHeader(task.path, pic)
					if err != nil {
						log.Error().Str(logFileIdentifier, task.path).Msgf("error while generating header: %v", err)
						cancel()
						return
					}
					toDumpChan <- printTask{header: header, pic: pic}
				}
			}
		}(i)
	}
	consumeWg.Wait()
	close(toDumpChan)
}

func (idxer *Indexer) Dump(ctx context.Context, root string, writer io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)

	toExtractChan := make(chan extractTask, idxer.toExtractChannelSize())
	toDumpChan := make(chan printTask, idxer.extractionThreadCount())
	var wg sync.WaitGroup
	wg.Add(2)

	go idxer.dump(ctx, cancel, &wg, toDumpChan, writer)
	go idxer.startConverters(ctx, cancel, &wg, toExtractChan, toDumpChan)

	err := common.BrowseImages(root, func(path string, info os.FileInfo) {
		toExtractChan <- extractTask{
			path: path,
			info: info,
		}
	})
	close(toExtractChan)
	if err != nil {
		cancel()
		return fmt.Errorf("error while browsing directory: %v", err)
	}
	wg.Wait()
	return nil
}

func (idxer *Indexer) convert(ctx context.Context, f string, fInfo os.FileInfo) (Model, error) {
	log.Info().Str(logFileIdentifier, f).Msg("Converting...")
	pic := Model{}

	metas := idxer.exif.ExtractMetadata(f)
	if len(metas) != 1 {
		return pic, fmt.Errorf("wrong metadata count (%v)", len(metas))
	}
	meta := metas[0]

	pic.ImportID = common.GetImportID(ctx)
	pic.Aperture = getFloat64(meta, apertureKey)
	pic.ISO = getInt64(meta, isoKey)
	pic.ShutterSpeed = getString(meta, shutterKey)
	pic.CameraModel = getString(meta, cameraKey)
	pic.LensModel = getString(meta, lensKey)
	pic.MimeType = getString(meta, mimeTypeKey)
	pic.Height = getInt64(meta, heightKey)
	pic.Width = getInt64(meta, widthKey)
	pic.Keywords = getStrings(meta, keywordsKey)
	pic.FileSize = uint64(fInfo.Size())
	pic.FileName = fInfo.Name()
	pic.Date = getDate(meta, captureDateKey)
	pic.GPS = getGPS(meta, gpsKey)

	components := strings.Split(f, string(os.PathSeparator))
	if len(components) > 1 {
		pic.Folder = components[len(components)-2]
	}

	return pic, nil
}

func (idxer *Indexer) Push(ctx context.Context, buffer *bytes.Buffer) error {
	u, err := url.Parse(idxer.conf.Url)
	if err != nil {
		return fmt.Errorf("error while parsing elasticsearch url (%v): %w", idxer.conf.Url, err)
	}
	u.Path = path.Join(u.Path, bulkSuffix)

	httpClient := &http.Client{
		Timeout: 60 * time.Second,
	}
	resp, err := httpClient.Post(u.String(), ndJsonMimeType, buffer)
	if err != nil {
		return fmt.Errorf("Error while pushing to Elasticsearch: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Wrong status code (%v)", resp.StatusCode)
	}

	return nil
}
