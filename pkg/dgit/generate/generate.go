package generate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"

	"github.com/davecgh/go-spew/spew"
	"github.com/google/go-containerregistry/authn"
	"github.com/google/go-containerregistry/name"
	"github.com/google/go-containerregistry/v1"
	"github.com/google/go-containerregistry/v1/random"
	"github.com/google/go-containerregistry/v1/remote"
	"github.com/google/go-containerregistry/v1/remote/transport"
	"github.com/google/go-containerregistry/v1/types"
	"github.com/google/go-github/github"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
)

type GithubClient struct {
	client *github.Client
}

var DefaultGithubClient = &GithubClient{}

func (g *GithubClient) Get() *github.Client {
	if g.client != nil {
		return g.client
	}
	token := os.Getenv("GITHUB_TOKEN")
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	g.client = github.NewClient(tc)
	return g.client
}

func GenerateManifest(owner, repo, ref string) error {
	client := DefaultGithubClient.Get()
	url, _, err := client.Repositories.GetArchiveLink(context.Background(), owner, repo, github.Tarball, &github.RepositoryContentGetOptions{
		Ref: ref,
	})
	if err != nil {
		return errors.Wrap(err, "getting archive link")
	}

	h, size, err := GetHash(url.String())
	if err != nil {
		return errors.Wrap(err, "getting hash of remote archive")
	}

	img, _ := random.Image(0, 0)
	manifest, err := img.Manifest()
	if err != nil {
		return errors.Wrap(err, "getting manifest")
	}

	manifest.Layers = append(manifest.Layers, v1.Descriptor{
		MediaType: types.DockerForeignLayer,
		Size:      size,
		Digest:    *h,
		URLs:      []string{url.String()},
	})

	dstRef, err := name.ParseReference("gcr.io/r2d4minikube/testgit", name.WeakValidation)
	if err != nil {
		return errors.Wrap(err, "getting source reference")
	}

	auth, err := authn.DefaultKeychain.Resolve(dstRef.Context().Registry)
	if err != nil {
		return err
	}

	remote.Write(dstRef, img, auth, http.DefaultTransport, remote.WriteOptions{})

	spew.Dump(img)

	if err := addTag(img, dstRef, auth, http.DefaultTransport); err != nil {
		return err
	}

	return nil
}

func GetHash(url string) (*v1.Hash, int64, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, 0, errors.Wrapf(err, "getting remote archive")
	}
	defer resp.Body.Close()
	h := sha256.New()

	if _, err := io.Copy(h, resp.Body); err != nil {
		return nil, 0, errors.Wrap(err, "calculating checksum")
	}
	return &v1.Hash{
		Algorithm: "sha256",
		Hex:       fmt.Sprintf("%x", h.Sum(nil)),
	}, int64(h.Size()), nil
}

func addTag(img v1.Image, targetRef name.Reference, auth authn.Authenticator, t http.RoundTripper) error {
	tr, err := transport.New(targetRef, auth, t, transport.PushScope)
	if err != nil {
		return err
	}

	data, err := img.RawManifest()
	if err != nil {
		return errors.Wrap(err, "getting raw manifest")
	}

	c := &http.Client{Transport: tr}
	u := url.URL{
		Scheme: transport.Scheme(targetRef.Context().Registry),
		Host:   targetRef.Context().RegistryStr(),
		Path:   fmt.Sprintf("/v2/%s/manifests/%s", targetRef.Context().RepositoryStr(), targetRef.Identifier()),
	}

	req, err := http.NewRequest(http.MethodPut, u.String(), bytes.NewReader(data))
	if err != nil {
		return errors.Wrap(err, "generating http request")
	}

	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		return nil
	default:
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("unrecognized status code during PUT: %v; %v", resp.Status, string(b))
	}
}
