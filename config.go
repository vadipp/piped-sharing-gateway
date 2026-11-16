package main

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	PublicOrigin  string
	AdminOrigin   string
	AdminSecret   string
	Backend       *url.URL
	MediaOrigin   *url.URL
	MediaUpstream *url.URL
}

func parseOrigin(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("expected an HTTP origin without credentials, path or query")
	}
	u.Path = ""
	return u, nil
}
func loadConfig() (Config, error) {
	var c Config
	var err error
	p, err := parseOrigin(os.Getenv("PUBLIC_ORIGIN"))
	if err != nil {
		return c, errors.New("invalid PUBLIC_ORIGIN")
	}
	c.PublicOrigin = p.String()
	a, err := parseOrigin(os.Getenv("ADMIN_ORIGIN"))
	if err != nil {
		return c, errors.New("invalid ADMIN_ORIGIN")
	}
	c.AdminOrigin = a.String()
	c.AdminSecret = os.Getenv("ADMIN_SECRET")
	if file := os.Getenv("ADMIN_SECRET_FILE"); file != "" {
		b, e := os.ReadFile(file)
		if e != nil {
			return c, e
		}
		c.AdminSecret = strings.TrimSpace(string(b))
	}
	if len(c.AdminSecret) < 32 {
		return c, errors.New("ADMIN_SECRET must contain at least 32 characters")
	}
	c.Backend, err = parseOrigin(os.Getenv("BACKEND_ORIGIN"))
	if err != nil {
		return c, errors.New("invalid BACKEND_ORIGIN")
	}
	c.MediaOrigin, err = parseOrigin(os.Getenv("MEDIA_ORIGIN"))
	if err != nil {
		return c, errors.New("invalid MEDIA_ORIGIN")
	}
	c.MediaUpstream, err = parseOrigin(os.Getenv("MEDIA_UPSTREAM"))
	if err != nil {
		return c, errors.New("invalid MEDIA_UPSTREAM")
	}
	return c, nil
}
