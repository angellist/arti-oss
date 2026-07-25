package config

import yaml "go.yaml.in/yaml/v3"

func yamlMarshal(v any) ([]byte, error) { return yaml.Marshal(v) }
