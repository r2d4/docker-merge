package merge

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"

	"github.com/containers/image/docker/daemon"
	"github.com/containers/image/types"
	"github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"matt-rickard.com/docker-merge/pkg/dgit/util"
)

type merger struct {
	tempDir  string
	buildDir string
	gitDir   string

	tag        name.Reference
	refs       []types.ImageReference
	outputFile string

	cli *client.Client
}

func New(tag string, refs []string) (*merger, error) {
	nameRef, err := name.ParseReference(tag)
	if err != nil {
		return nil, errors.Wrap(err, "name")
	}
	imgRefs, err := parseReferences(refs)
	if err != nil {
		return nil, errors.Wrap(err, "refs")
	}
	tempDir, err := ioutil.TempDir("", "")
	if err != nil {
		return nil, errors.Wrap(err, "create temp dir")
	}
	buildDir := path.Join(tempDir, "build")
	gitDir := path.Join(tempDir, "git")
	if err := os.Mkdir(buildDir, 0755); err != nil {
		return nil, errors.Wrap(err, "mkdir")
	}
	if err := os.Mkdir(gitDir, 0755); err != nil {
		return nil, errors.Wrap(err, "mkdir")
	}

	cli, err := client.NewEnvClient()
	if err != nil {
		return nil, errors.Wrap(err, "docker client")
	}

	return &merger{
		gitDir:   gitDir,
		tempDir:  tempDir,
		buildDir: buildDir,
		refs:     imgRefs,
		tag:      nameRef,
		cli:      cli,
	}, nil
}

func (m *merger) Close() error {
	if err := os.RemoveAll(m.tempDir); err != nil {
		return err
	}
	return nil
}

func MergeImages(tag string, refs []string) error {
	if len(refs) < 2 {
		return fmt.Errorf("need to provide more than one image")
	}
	m, err := New(tag, refs)
	if err != nil {
		return errors.Wrap(err, "merger")
	}
	// defer m.Close()
	if err := m.createRepo(); err != nil {
		return errors.Wrap(err, "create repo")
	}
	if err := m.merge(); err != nil {
		return errors.Wrap(err, "merge")
	}
	if err := m.createImage(); err != nil {
		return errors.Wrap(err, "create image")
	}
	if err := m.loadImg(); err != nil {
		return errors.Wrap(err, "load img")
	}
	return nil
}

func parseReferences(refs []string) ([]types.ImageReference, error) {
	var imageRefs []types.ImageReference
	for _, img := range refs {
		ref, err := daemon.ParseReference(img)
		if err != nil {
			return nil, errors.Wrap(err, "parsing base image reference")
		}
		imageRefs = append(imageRefs, ref)
	}
	return imageRefs, nil
}

func (m *merger) loadImg() error {
	r, err := os.Open(m.outputFile)
	if err != nil {
		return errors.Wrap(err, "opening output file")
	}
	defer r.Close()
	resp, err := m.cli.ImageLoad(context.Background(), r, true)
	defer resp.Body.Close()
	if err != nil {
		return errors.Wrap(err, "loading img")
	}
	return nil
}

func (m *merger) merge() error {
	baseImg := m.refs[0]
	checkOut := exec.Command("git", "checkout", baseImg.DockerReference().Name())
	checkOut.Dir = m.gitDir
	if _, _, err := util.RunCommand(checkOut, nil); err != nil {
		return errors.Wrap(err, "checkout branch")
	}
	for i := 1; i < len(m.refs); i++ {
		mergeCmd := exec.Command("git", "merge", m.refs[i].DockerReference().Name(), "-X", "ours")
		mergeCmd.Dir = m.gitDir
		if err := mergeCmd.Run(); err != nil {
			return errors.Wrap(err, "merge branch")
		}
	}
	return nil
}

func (m *merger) createImage() error {
	base := empty.Image
	tarPath := path.Join(m.buildDir, "image.tar.gz")
	tarDirCmd := exec.Command("tar", "--exclude", ".git", "-C", m.gitDir, "-zcvf", tarPath, ".")
	tarDirCmd.Dir = m.gitDir
	if _, _, err := util.RunCommand(tarDirCmd, nil); err != nil {
		return errors.Wrap(err, "tar dir")
	}
	layer, err := tarball.LayerFromFile(tarPath)
	if err != nil {
		return errors.Wrap(err, "layer from file")
	}
	img, err := mutate.AppendLayers(base, layer)
	if err != nil {
		return errors.Wrap(err, "appending layer")
	}
	newConfig, err := img.ConfigFile()
	if err != nil {
		return errors.Wrap(err, "getting cfg")
	}
	newConfig.Architecture = "amd64"
	img, err = mutate.Config(img, newConfig.Config)
	if err != nil {
		return errors.Wrap(err, "mutate config")
	}
	m.outputFile = path.Join(m.buildDir, "output.tar.gz")
	if err := tarball.WriteToFile(m.outputFile, m.tag, img); err != nil {
		return errors.Wrap(err, "writing tar image")
	}
	return nil
}

func (m *merger) createRepo() error {
	if err := os.MkdirAll(m.gitDir, 0755); err != nil {
		return errors.Wrap(err, "creating staging repo path")
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = m.gitDir
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, "init git repo")
	}

	superRoot := exec.Command("git", "commit", "--allow-empty", "-m", `'super-root'`)
	superRoot.Dir = m.gitDir
	if err := superRoot.Run(); err != nil {
		return errors.Wrap(err, "creating super root commit")
	}

	for _, ref := range m.refs {
		if err := processImage(ref, m.gitDir, m.gitDir); err != nil {
			return errors.Wrap(err, "processing image")
		}
	}

	return nil
}

func processImage(a types.ImageReference, git, dir string) error {
	branchCmd := exec.Command("git", "checkout", "-b", a.DockerReference().Name(), "master")
	branchCmd.Dir = dir
	if _, _, err := util.RunCommand(branchCmd, nil); err != nil {
		return errors.Wrap(err, "creating branch")
	}
	baseImg, err := a.NewImage(&types.SystemContext{})
	if err != nil {
		return err
	}
	defer baseImg.Close()

	baseImgSrc, err := a.NewImageSource(&types.SystemContext{})
	if err != nil {
		return err
	}

	for _, li := range baseImg.LayerInfos() {
		b, _, err := baseImgSrc.GetBlob(li)
		if err != nil {
			return errors.Wrap(err, "getting blob")
		}
		defer b.Close()
		blobPath := filepath.Join(dir, li.Digest.String()+".tar.gz")
		f, err := os.Create(blobPath)
		if err != nil {
			return errors.Wrap(err, "opening file")
		}
		defer f.Close()
		if _, err := io.Copy(f, b); err != nil {
			return errors.Wrap(err, "writing file")
		}

		b.Close()

		logrus.Infof("Uncompressing %s...", blobPath)
		if out, err := exec.Command("tar", "-xvf", blobPath, "-C", git).CombinedOutput(); err != nil {
			logrus.Warnf("uncompressing tarball to git repo: %s %s", out, err)
		}
		logrus.Infof("Uncompressed %s.", blobPath)
		cmd := exec.Command("git", "add", "-A")
		cmd.Dir = git
		if err := cmd.Run(); err != nil {
			return errors.Wrap(err, "git add")
		}

		// commit and quit
		commitCmd := exec.Command("git", "commit", "-am", fmt.Sprintf(`'%s'`, li.Digest.String()), "--date", "1523820568 -0700", "--author", "test <email>")
		commitCmd.Dir = git
		commitCmd.Env = os.Environ()
		commitCmd.Env = append(commitCmd.Env, `GIT_COMMITTER_DATE="1523820568 -0700"`)
		if out, err := commitCmd.CombinedOutput(); err != nil {
			return errors.Wrapf(err, "committing layer: %s", out)
		}
	}
	return nil
}
