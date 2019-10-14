package indexer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"strconv"

	exif "github.com/barasher/go-exiftool"
	"github.com/barasher/picdexer/internal/model"
	"github.com/sirupsen/logrus"
)

const (
	APERTURE_KEY    = "Aperture"
	SHUTTER_KEY     = "ShutterSpeed"
	KEYWORDS_KEY    = "Keywords"
	CAMERA_KEY      = "Model"
	LENS_KEY        = "LensModel"
	MIMETYPE_KEY    = "MIMEType"
	HEIGHT_KEY      = "ImageHeight"
	WIDTH_KEY       = "ImageWidth"
	CAPTUREDATE_KEY = "CreateDate"
	GPS_KEY         = "GPSPosition"

	SRC_DATE_FORMAT = "2006:01:02 15:04:05"

	ES_BULK_LINE_HEADER = "{ \"index\":{} }"

	IMAGE_MIME_TYPE = "image/"
)

type Indexer struct {
	input string
	exif  *exif.Exiftool
}

func NewIndexer(opts ...func(*Indexer) error) (*Indexer, error) {
	idxer := &Indexer{}
	for _, opt := range opts {
		if err := opt(idxer); err != nil {
			return nil, fmt.Errorf("Initialization error: %v", err)
		}
	}

	et, err := exif.NewExiftool()
	if err != nil {
		return idxer, fmt.Errorf("error while initializing metadata extractor: %v", err)
	}
	idxer.exif = et

	return idxer, nil
}

func Input(input string) func(*Indexer) error {
	return func(idxer *Indexer) error {
		idxer.input = input
		return nil
	}
}

func (idxer *Indexer) Close() error {
	if idxer.exif != nil {
		if err := idxer.exif.Close(); err != nil {
			logrus.Errorf("error while closing exiftool: %v", err)
		}
	}
	return nil
}

func (idxer *Indexer) Index() error {
	jsonEncoder := json.NewEncoder(os.Stdout)
	err := filepath.Walk(idxer.input, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			pic, err := idxer.convert(path, info)
			if err != nil {
				logrus.Errorf("%v: %v", path, err)
			} else {
				if pic.MimeType != nil && strings.HasPrefix(*pic.MimeType, IMAGE_MIME_TYPE) {
					fmt.Fprintln(os.Stdout, ES_BULK_LINE_HEADER)
					if err := jsonEncoder.Encode(pic); err != nil {
						logrus.Errorf("error while encoding json: %v", err)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("error while browsing directory: %v", err)
	}

	return nil
}

func getFloat(m exif.FileMetadata, k string) *float64 {
	v, found := m.Fields[k]
	if !found {
		return nil
	}
	v2 := v.(float64)
	return &v2
}

func getString(m exif.FileMetadata, k string) *string {
	v, found := m.Fields[k]
	if found {
		if v2, isStr := v.(string); isStr {
			return &v2
		} else if v3, isFloat := v.(float64); isFloat {
			sv3 := fmt.Sprintf("%v", v3)
			return &sv3
		}
	}
	return nil
}

func getUint64FromFloat64(m exif.FileMetadata, k string) *uint64 {
	v, found := m.Fields[k]
	if !found {
		return nil
	}
	v2 := uint64(v.(float64))
	return &v2
}

func (idxer *Indexer) convert(f string, fInfo os.FileInfo) (model.Model, error) {
	logrus.Infof("%v", f)
	pic := model.Model{}

	metas := idxer.exif.ExtractMetadata(f)
	if len(metas) != 1 {
		return pic, fmt.Errorf("wrong metadata count (%v)", len(metas))
	}
	meta := metas[0]

	pic.Aperture = getFloat(meta, APERTURE_KEY)
	pic.ShutterSpeed = getString(meta, SHUTTER_KEY)
	pic.CameraModel = getString(meta, CAMERA_KEY)
	pic.LensModel = getString(meta, LENS_KEY)
	pic.MimeType = getString(meta, MIMETYPE_KEY)
	pic.Height = getUint64FromFloat64(meta, HEIGHT_KEY)
	pic.Width = getUint64FromFloat64(meta, WIDTH_KEY)
	pic.FileSize = uint64(fInfo.Size())
	pic.FileName = fInfo.Name()

	components := strings.Split(f, string(os.PathSeparator))
	if len(components) > 1 {
		pic.Folder = components[len(components)-2]
	}

	rawKws, found := meta.Fields[KEYWORDS_KEY]
	if found {
		var kws []string
		if interfaceSlices, is := rawKws.([]interface{}); is {
			kws = make([]string, len(interfaceSlices))
			for i, v := range interfaceSlices {
				kws[i] = v.(string)
			}
		} else if str, is := rawKws.(string); is {
			kws = append(kws, str)
		}
		pic.Keywords = kws
	}

	rawDate, found := meta.Fields[CAPTUREDATE_KEY]
	if found {
		if d, err := time.Parse(SRC_DATE_FORMAT, rawDate.(string)); err != nil {
			return pic, fmt.Errorf("error while parsing date (%v): %v", rawDate.(string), err)
		} else {
			d2 := strconv.FormatInt(d.Unix()*1000, 10)
			pic.Date = &d2
		}
	}

	if gpsVal, found := meta.Fields[GPS_KEY]; found {
		lat, long, err := convertGPSCoordinates(gpsVal.(string))
		if err != nil {
			return pic, fmt.Errorf("error while converting gps coordinates (%v): %v", gpsVal, err)
		}
		gps := fmt.Sprintf("%v,%v", lat, long)
		pic.GPS = &gps
	}

	return pic, nil
}

func degMinSecToDecimal(deg, min, sec, let string) (float32, error) {
	var fDeg, fMin, fSec float64
	var err error
	if fDeg, err = strconv.ParseFloat(deg, 32); err != nil {
		return 0, fmt.Errorf("error while parsing deg %v as float", deg)
	}
	if fMin, err = strconv.ParseFloat(min, 32); err != nil {
		return 0, fmt.Errorf("error while parsing min %v as float", min)
	}
	if fSec, err = strconv.ParseFloat(sec, 32); err != nil {
		return 0, fmt.Errorf("error while parsing sec %v as float", sec)
	}
	var mult float64
	switch {
	case let == "S" || let == "W":
		mult = -1
	case let == "N" || let == "E":
		mult = 1
	default:
		return 0, fmt.Errorf("Unsupported letter (%v)", let)
	}
	return float32((fDeg + fMin/60 + fSec/3600) * mult), nil
}

func skipLastChar(src string) string {
	return src[:len(src)-1]
}

func convertGPSCoordinates(latLong string) (float32, float32, error) {
	sub := strings.Split(latLong, " ")
	if len(sub) != 10 {
		return 0, 0, fmt.Errorf("Parsing inconsistency (%v): %v elements parsed", latLong, len(sub))
	}
	lat, err := degMinSecToDecimal(sub[0], skipLastChar(sub[2]), skipLastChar(sub[3]), skipLastChar(sub[4]))
	if err != nil {
		return 0, 0, fmt.Errorf("error while converting latitude (%v): %v", latLong, err)
	}
	long, err := degMinSecToDecimal(sub[5], skipLastChar(sub[7]), skipLastChar(sub[8]), sub[9])
	if err != nil {
		return 0, 0, fmt.Errorf("error while converting longitude (%v): %v", latLong, err)
	}
	return lat, long, nil
}
