package main

import "encoding/json"

func jsonRoundTrip(c config) (config, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return config{}, err
	}
	var out config
	err = json.Unmarshal(b, &out)
	return out, err
}
