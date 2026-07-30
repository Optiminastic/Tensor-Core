package shopify

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
)

// ProductImage is one image to attach to the product: its bytes plus the
// filename and MIME type Shopify needs to stage the upload.
type ProductImage struct {
	Filename string
	MimeType string
	Data     []byte
}

const stagedUploadsMutation = `mutation StageUploads($input: [StagedUploadInput!]!) {
  stagedUploadsCreate(input: $input) {
    stagedTargets { url resourceUrl parameters { name value } }
    userErrors { field message }
  }
}`

type stagedParameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type stagedTarget struct {
	URL         string            `json:"url"`
	ResourceURL string            `json:"resourceUrl"`
	Parameters  []stagedParameter `json:"parameters"`
}

// uploadImages stages each image with Shopify and uploads its bytes, returning
// the resource URLs to pass as product media. Image bytes go to Shopify's own
// storage, so Tensor never needs to host them publicly. An empty input is a
// no-op.
func (c *Client) uploadImages(ctx context.Context, shop, token string, images []ProductImage) ([]string, error) {
	if len(images) == 0 {
		return nil, nil
	}

	input := make([]map[string]any, 0, len(images))
	for _, img := range images {
		input = append(input, map[string]any{
			"filename": img.Filename, "mimeType": img.MimeType,
			"resource": "IMAGE", "httpMethod": "POST",
		})
	}

	var out struct {
		Data struct {
			StagedUploadsCreate struct {
				StagedTargets []stagedTarget `json:"stagedTargets"`
				UserErrors    []userError    `json:"userErrors"`
			} `json:"stagedUploadsCreate"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, stagedUploadsMutation, map[string]any{"input": input}, &out); err != nil {
		return nil, err
	}
	if msg := firstUserError(out.Data.StagedUploadsCreate.UserErrors); msg != "" {
		return nil, apiErr("Shopify rejected the image upload: %s", msg)
	}
	targets := out.Data.StagedUploadsCreate.StagedTargets
	if len(targets) != len(images) {
		return nil, apiErr("Shopify returned %d upload targets for %d images", len(targets), len(images))
	}

	resources := make([]string, 0, len(images))
	for i, target := range targets {
		if err := c.postToTarget(ctx, target, images[i]); err != nil {
			return nil, err
		}
		resources = append(resources, target.ResourceURL)
	}
	return resources, nil
}

// postToTarget uploads one image's bytes to its staged target. Shopify's target
// is an object store that requires the returned parameters as form fields, in
// order, with the file field last.
func (c *Client) postToTarget(ctx context.Context, target stagedTarget, img ProductImage) error {
	body := new(bytes.Buffer)
	form := multipart.NewWriter(body)
	for _, p := range target.Parameters {
		if err := form.WriteField(p.Name, p.Value); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("file", img.Filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(img.Data); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return &APIError{msg: fmt.Sprintf("could not upload image to Shopify: %v", err), err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiErr("Shopify image upload failed (%d)", resp.StatusCode)
	}
	return nil
}
