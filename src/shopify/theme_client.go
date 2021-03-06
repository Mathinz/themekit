package shopify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Shopify/themekit/src/env"
	"github.com/Shopify/themekit/src/file"
	"github.com/Shopify/themekit/src/httpify"
)

var (
	// ErrCriticalFile will be returned when trying to remove a critical file
	ErrCriticalFile = errors.New("this file is critical and removing it would cause your theme to become non-functional")
	// ErrNotPartOfTheme will be returned when trying to alter a filepath that does not exist in the theme
	ErrNotPartOfTheme = errors.New("this file is not part of your theme")
	// ErrMalformedResponse will be returned if we could not unmarshal the response from shopify
	ErrMalformedResponse = errors.New("received a malformed response")
	// ErrZipPathRequired is returned if a source path was not provided to create a new theme
	ErrZipPathRequired = errors.New("theme zip path is required")
	// ErrInfoWithoutThemeID will be returned if GetInfo is called on a live theme
	ErrInfoWithoutThemeID = errors.New("cannot get info without a theme id")
	// ErrPublishWithoutThemeID will be returned if PublishTheme is called on a live theme
	ErrPublishWithoutThemeID = errors.New("cannot publish a theme without a theme id set")
	// ErrThemeNotFound will be returned if trying to get a theme that does not exist
	ErrThemeNotFound = errors.New("requested theme was not found")
	// ErrShopDomainNotFound will be returned if you are getting shop info on an invalid domain
	ErrShopDomainNotFound = errors.New("provided myshopify domain does not exist")
	// ErrMissingAssetName is returned from delete when an invalid key was provided
	ErrMissingAssetName = errors.New("asset has no name so could not be processes")
	// ErrThemeNameRequired is returned when trying to create a theme with a blank name
	ErrThemeNameRequired = errors.New("theme name is required to create a theme")

	shopifyAPILimit = time.Second / 2 // 2 calls per second
)

// Theme represents a shopify theme.
type Theme struct {
	ID          int64  `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Role        string `json:"role,omitempty"`
	Previewable bool   `json:"previewable,omitempty"`
	Processing  bool   `json:"processing,omitempty"`
}

// Shop information for the domain your are currently working on
type Shop struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	City    string `json:"city"`
	Country string `json:"country"`
	Desc    string `json:"description"`
}

type themeResponse struct {
	Theme  Theme               `json:"theme"`
	Errors map[string][]string `json:"errors"`
}

type themesResponse struct {
	Themes []Theme `json:"themes"`
}

type assetResponse struct {
	Asset  Asset               `json:"asset"`
	Errors map[string][]string `json:"errors"`
}

type assetsResponse struct {
	Assets []Asset `json:"assets"`
}

type reqErr struct {
	Errors string `json:"errors"`
}

type httpAdapter interface {
	Get(string) (*http.Response, error)
	Post(string, interface{}) (*http.Response, error)
	Put(string, interface{}) (*http.Response, error)
	Delete(string) (*http.Response, error)
}

// Client is the interactor with the shopify server. All actions are processed
// with the client.
type Client struct {
	themeID string
	filter  file.Filter
	http    httpAdapter
}

// NewClient will build a new theme client from a configuration and a theme event
// channel. The channel is used for logging all events. The configuration specifies how
// the client will behave.
func NewClient(e *env.Env) (Client, error) {
	filter, err := file.NewFilter(e.Directory, e.IgnoredFiles, e.Ignores)
	if err != nil {
		return Client{}, err
	}

	http, err := httpify.NewClient(httpify.Params{
		Domain:   e.Domain,
		Password: e.Password,
		Proxy:    e.Proxy,
		Timeout:  e.Timeout,
		APILimit: shopifyAPILimit,
	})
	if err != nil {
		return Client{}, err
	}

	return Client{
		themeID: e.ThemeID,
		http:    http,
		filter:  filter,
	}, nil
}

// GetShop will return information for the shop you are working on
func (c Client) GetShop() (Shop, error) {
	resp, err := c.http.Get("/meta.json")
	if err != nil {
		return Shop{}, err
	} else if resp.StatusCode == 404 {
		return Shop{}, ErrShopDomainNotFound
	}

	var shop Shop
	if err := unmarshalResponse(resp.Body, &shop); err != nil {
		return Shop{}, err
	}

	return shop, nil
}

// Themes will return all the available themes on a domain.
func (c Client) Themes() ([]Theme, error) {
	resp, err := c.http.Get("/admin/themes.json")
	if err != nil {
		return []Theme{}, err
	}

	var r themesResponse
	if err := unmarshalResponse(resp.Body, &r); err != nil {
		return []Theme{}, err
	}

	return r.Themes, nil
}

// CreateNewTheme will create a unpublished new theme on your shopify store and then
// set the theme id on this theme client to the one recently created.
func (c *Client) CreateNewTheme(name string) (theme Theme, err error) {
	if name == "" {
		return Theme{}, ErrThemeNameRequired
	}

	resp, err := c.http.Post("/admin/themes.json", map[string]interface{}{"theme": Theme{Name: name}})
	if err != nil {
		return Theme{}, err
	}

	var r themeResponse
	if err = unmarshalResponse(resp.Body, &r); err != nil {
		return Theme{}, err
	}

	if len(r.Errors) > 0 {
		return Theme{}, errors.New(toSentence(toMessages(r.Errors)))
	}

	c.themeID = fmt.Sprintf("%d", r.Theme.ID)
	return r.Theme, err
}

// GetInfo will return the theme data for the clients theme.
func (c Client) GetInfo() (Theme, error) {
	if c.themeID == "" {
		return Theme{}, ErrInfoWithoutThemeID
	}

	resp, err := c.http.Get(fmt.Sprintf("/admin/themes/%s.json", c.themeID))
	if err != nil {
		return Theme{}, err
	} else if resp.StatusCode == 404 {
		return Theme{}, ErrThemeNotFound
	}

	var r themeResponse
	if err := unmarshalResponse(resp.Body, &r); err != nil {
		return Theme{}, err
	}

	return r.Theme, nil
}

// PublishTheme will update the theme to be role main
func (c Client) PublishTheme() error {
	if c.themeID == "" {
		return ErrPublishWithoutThemeID
	}

	resp, err := c.http.Put(
		fmt.Sprintf("/admin/themes/%s.json", c.themeID),
		map[string]Theme{"theme": {Role: "main"}},
	)
	if err != nil {
		return err
	} else if resp.StatusCode == 404 {
		return ErrThemeNotFound
	}

	var r themeResponse
	if err = unmarshalResponse(resp.Body, &r); err != nil {
		return err
	}

	if len(r.Errors) > 0 {
		return errors.New(toSentence(toMessages(r.Errors)))
	}

	return nil
}

// GetAllAssets will return a slice of remote assets from the shopify servers. The
// assets are sorted and any ignored files based on your config are filtered out.
// The assets returned will not have any data, only ID and filenames. This is because
// fetching all the assets at one time is not a good idea.
func (c Client) GetAllAssets() ([]string, error) {
	resp, err := c.http.Get(c.assetPath(map[string]string{"fields": "key"}))
	if err != nil {
		return []string{}, err
	} else if resp.StatusCode == 404 {
		return []string{}, ErrThemeNotFound
	}

	var r assetsResponse
	if err := unmarshalResponse(resp.Body, &r); err != nil {
		return []string{}, err
	}

	filteredAssets := []string{}
	sort.Slice(r.Assets, func(i, j int) bool { return r.Assets[i].Key < r.Assets[j].Key })
	for index, asset := range r.Assets {
		if !c.filter.Match(asset.Key) && (index == len(r.Assets)-1 || r.Assets[index+1].Key != asset.Key+".liquid") {
			filteredAssets = append(filteredAssets, asset.Key)
		}
	}

	return filteredAssets, nil
}

// GetAsset will fetch a single remote asset from the remote shopify servers.
func (c Client) GetAsset(filename string) (Asset, error) {
	resp, err := c.http.Get(c.assetPath(map[string]string{"asset[key]": filename}))
	if err != nil {
		return Asset{}, err
	} else if resp.StatusCode == 404 {
		return Asset{}, ErrNotPartOfTheme
	}

	var r assetResponse
	if err := unmarshalResponse(resp.Body, &r); err != nil {
		return Asset{}, err
	}

	return r.Asset, nil
}

// CreateAsset will take an asset and will return  when the asset has been created.
// If there was an error, in the request then error will be defined otherwise the
//response will have the appropropriate data for usage.
func (c Client) CreateAsset(asset Asset) error {
	return c.UpdateAsset(asset)
}

// UpdateAsset will take an asset and will return  when the asset has been updated.
// If there was an error, in the request then error will be defined otherwise the
//response will have the appropropriate data for usage.
func (c Client) UpdateAsset(asset Asset) error {
	resp, err := c.http.Put(c.assetPath(map[string]string{}), map[string]Asset{"asset": asset})
	if err != nil {
		return err
	} else if resp.StatusCode == 404 {
		return ErrNotPartOfTheme
	}

	var r assetResponse
	if err := unmarshalResponse(resp.Body, &r); err != nil {
		return err
	}

	if len(r.Errors) > 0 {
		if _, ok := r.Errors["asset"]; ok {
			if resp.StatusCode == 422 && strings.Contains(r.Errors["asset"][0], "Cannot overwrite generated asset") {
				// No need to check the error because if it fails then remove will be tried again.
				c.DeleteAsset(Asset{Key: asset.Key + ".liquid"})
				return c.UpdateAsset(asset)
			}
			return errors.New(toSentence(r.Errors["asset"]))
		}
		return errors.New(toSentence(toMessages(r.Errors)))
	}

	return nil
}

// DeleteAsset will take an asset and will return  when the asset has been deleted.
// If there was an error, in the request then error will be defined otherwise the
//response will have the appropropriate data for usage.
func (c Client) DeleteAsset(asset Asset) error {
	resp, err := c.http.Delete(c.assetPath(map[string]string{"asset[key]": asset.Key}))
	if err != nil {
		return err
	} else if resp.StatusCode == 403 {
		return ErrCriticalFile
	} else if resp.StatusCode == 404 {
		return ErrNotPartOfTheme
	} else if resp.StatusCode == 406 {
		return ErrMissingAssetName
	}

	var r assetResponse
	if err := unmarshalResponse(resp.Body, &r); err != nil {
		return err
	}

	if len(r.Errors) > 0 {
		return errors.New(toSentence(toMessages(r.Errors)))
	}

	return nil
}

func (c Client) assetPath(query map[string]string) string {
	formatted := "/admin/assets.json"
	if c.themeID != "" {
		formatted = fmt.Sprintf("/admin/themes/%s/assets.json", c.themeID)
	}

	if len(query) > 0 {
		queryParams := url.Values{}
		for key, value := range query {
			queryParams.Set(key, value)
		}
		formatted = fmt.Sprintf("%s?%s", formatted, queryParams.Encode())
	}

	return formatted
}

func unmarshalResponse(body io.ReadCloser, data interface{}) error {
	reqBody, err := ioutil.ReadAll(body)
	if err != nil {
		return ErrMalformedResponse
	}
	err = body.Close()
	if err != nil {
		return err
	}
	var re reqErr
	mainErr := json.Unmarshal(reqBody, data)
	basicErr := json.Unmarshal(reqBody, &re)
	if mainErr != nil && basicErr != nil {
		return ErrMalformedResponse
	}
	if len(re.Errors) > 0 {
		return errors.New(re.Errors)
	}
	return nil
}

func toMessages(a map[string][]string) []string {
	out := []string{}
	for attr, errs := range a {
		for _, err := range errs {
			out = append(out, attr+" "+err)
		}
	}
	return out
}

func toSentence(a []string) string {
	switch len(a) {
	case 0:
		return ""
	case 1:
		return a[0]
	case 2:
		return a[0] + " and " + a[1]
	}
	return strings.Join(a[:len(a)-1], ", ") + ", and " + a[len(a)-1]
}
