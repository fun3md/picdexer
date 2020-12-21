package cmd

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	LogLevel      string            `json:"loggingLevel"`
	Elasticsearch ElasticsearchConf `json:"elasticsearch"`
	Binary        BinaryConf        `json:"binary"`
}

type ElasticsearchConf struct {
	Url         string `json:"url"`
	ThreadCount int    `json:"threadCount"`
	BulkSize    int    `json:"bulkSize"`
}

type BinaryConf struct {
	Url         string `json:"url"`
	Height      int    `json:"height"`
	Width       int    `json:"width"`
	ThreadCount int    `json:"threadCount"`
	WorkingDir  string `json:"workingDir"`
}

func LoadConf(f string) (Config, error) {
	conf := Config{}
	confReader, err := os.Open(f)
	if err != nil {
		return conf, fmt.Errorf("Error while opening configuration file %v: %w", f, err)
	}
	defer confReader.Close()
	err = json.NewDecoder(confReader).Decode(&conf)
	if err != nil {
		return conf, fmt.Errorf("Error while unmarshaling configuration file %v: %w", f, err)
	}
	return conf, nil
}
