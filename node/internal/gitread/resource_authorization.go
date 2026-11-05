package gitread

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

const maxResourceApprovalBytes = 32 << 10

type resourceAuthorizationState struct{ directory string }

type resourceApproval struct {
	Format            int    `json:"format"`
	ResourceID        string `json:"resource_id"`
	ValidationVersion int64  `json:"validation_version"`
	CanonicalPath     string `json:"canonical_path"`
	IdentityHash      string `json:"identity_hash"`
}

func resourceAuthorizationError() error {
	return gitError("path_out_of_scope", "Resource authorization is unavailable or changed; explicitly revalidate the resource.", false)
}

// ApproveResource is an explicit resource-validation write, never a discovery
// fallback. The caller must use the control plane's validation version and only
// call it after the user's resource.validate operation succeeds. A changed
// directory cannot overwrite an approval at the same or an older version.
// The path must already be canonical (apart from fixed macOS system aliases).
func (s *Service) ApproveResource(ctx context.Context, resourceID, directory string, validationVersion int64) error {
	done, err := s.beginServiceOperation()
	if err != nil {
		return err
	}
	defer done()
	if s.resourceAuthorizations == nil || strings.TrimSpace(resourceID) == "" ||
		len(resourceID) > nodeprotocol.MaxGitResourceIDBytes || validationVersion <= 0 ||
		len(directory) > nodeprotocol.MaxGitResourcePathBytes {
		return resourceAuthorizationError()
	}
	canonical, err := canonicalResourceDirectory(directory)
	if err != nil {
		return resourceAuthorizationError()
	}
	identity, err := captureStablePathIdentity(canonical)
	if err != nil {
		return resourceAuthorizationError()
	}
	defer identity.close()
	hash, err := resourceIdentityHash(identity)
	if err != nil {
		return resourceAuthorizationError()
	}
	root, stateIdentity, err := s.resourceAuthorizations.open(true)
	if err != nil {
		return resourceAuthorizationError()
	}
	defer root.Close()
	defer stateIdentity.close()
	name := resourceApprovalName(resourceID)
	lock, err := openResourceApprovalLock(stateIdentity.handle, name+".lock")
	if err != nil {
		return resourceAuthorizationError()
	}
	defer lock.Close()
	if err := lockResourceApproval(ctx, lock); err != nil {
		return err
	}
	defer unlockResourceApproval(lock)
	previous, err := readResourceApproval(ctx, stateIdentity.handle, name, resourceID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return resourceAuthorizationError()
	}
	next := resourceApproval{Format: 1, ResourceID: resourceID, ValidationVersion: validationVersion,
		CanonicalPath: canonical, IdentityHash: hash}
	if err == nil {
		if previous == next {
			if identity.validate() != nil || stateIdentity.validate() != nil {
				return resourceAuthorizationError()
			}
			return nil
		}
		if validationVersion <= previous.ValidationVersion {
			return resourceAuthorizationError()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if identity.validate() != nil || stateIdentity.validate() != nil {
		return resourceAuthorizationError()
	}
	if err := writeResourceApproval(root, stateIdentity.handle, name, next); err != nil {
		return resourceAuthorizationError()
	}
	if identity.validate() != nil || stateIdentity.validate() != nil {
		// Retain a stale record, never silently approve whatever replaced it.
		return resourceAuthorizationError()
	}
	return nil
}

func (s *Service) authorizedResourceHash(ctx context.Context, resources []nodeprotocol.GitResourceRequest) (string, error) {
	bindings := make([]string, 0, len(resources))
	for _, resource := range resources {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		canonical, err := canonicalResourceDirectory(resource.Path)
		if err != nil {
			return "", resourceAuthorizationError()
		}
		if s.resourceAuthorizations == nil {
			continue // Standalone read clients still enforce the no-symlink boundary.
		}
		root, stateIdentity, err := s.resourceAuthorizations.open(false)
		if err != nil {
			return "", resourceAuthorizationError()
		}
		approval, err := readResourceApproval(ctx, stateIdentity.handle, resourceApprovalName(resource.ResourceID), resource.ResourceID)
		stateErr := stateIdentity.validate()
		root.Close()
		stateIdentity.close()
		if err != nil || stateErr != nil || approval.CanonicalPath != canonical {
			return "", resourceAuthorizationError()
		}
		identity, err := captureStablePathIdentity(canonical)
		if err != nil {
			return "", resourceAuthorizationError()
		}
		hash, identityErr := resourceIdentityHash(identity)
		identity.close()
		if identityErr != nil || hash != approval.IdentityHash {
			return "", resourceAuthorizationError()
		}
		bindings = append(bindings, stableSHA256ID("resource-binding", approval.ResourceID, approval.CanonicalPath,
			strconv.FormatInt(approval.ValidationVersion, 10), approval.IdentityHash))
	}
	if s.resourceAuthorizations == nil {
		return hashResources(resources), nil
	}
	// Discovery ordering/labels belong to its cache key, not to the Provider's
	// authorization. Reordering the same approved roots must retain its scoped
	// CWD/native-session key, while every approval version/identity still fences it.
	sort.Strings(bindings)
	return hashResources(resources) + ":" + stableSHA256ID("approved-resources", bindings...), nil
}

func (s *Service) resourceAuthorizationScopeHash(resourcesHash string) string {
	if s.resourceAuthorizations != nil {
		_, scope, _ := strings.Cut(resourcesHash, ":")
		return scope // Bind native-session scope to the durable approvals too.
	}
	return ""
}

func resourceApprovalName(resourceID string) string {
	return stableSHA256ID("resource-approval", resourceID) + ".json"
}

func (state *resourceAuthorizationState) open(create bool) (*os.Root, stablePathIdentity, error) {
	if !filepath.IsAbs(state.directory) {
		return nil, stablePathIdentity{}, errors.New("private approval directory must be absolute")
	}
	directory, err := normalizeTrustedResourceAlias(filepath.Clean(state.directory))
	if err != nil {
		return nil, stablePathIdentity{}, err
	}
	parent, err := canonicalResourceDirectory(filepath.Dir(directory))
	if err != nil {
		return nil, stablePathIdentity{}, err
	}
	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		return nil, stablePathIdentity{}, err
	}
	defer parentRoot.Close()
	if create {
		if err := parentRoot.Mkdir(filepath.Base(directory), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, stablePathIdentity{}, err
		}
	}
	identity, err := captureStablePathIdentity(directory)
	if err != nil {
		return nil, stablePathIdentity{}, err
	}
	info, err := identity.handle.Stat()
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		identity.close()
		return nil, stablePathIdentity{}, errors.New("approval directory is not private")
	}
	root, err := os.OpenRoot(directory)
	if err != nil || identity.validate() != nil {
		if root != nil {
			root.Close()
		}
		identity.close()
		return nil, stablePathIdentity{}, errors.New("approval directory changed")
	}
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(rootInfo, info) {
		root.Close()
		identity.close()
		return nil, stablePathIdentity{}, errors.New("approval directory changed while opening")
	}
	return root, identity, nil
}

func readResourceApproval(ctx context.Context, directory *os.File, name, resourceID string) (resourceApproval, error) {
	file, info, err := openSandboxRegularAt(directory, name)
	if err != nil {
		return resourceApproval{}, err
	}
	defer file.Close()
	if info.Mode().Perm()&0o077 != 0 {
		return resourceApproval{}, errors.New("approval record is not private")
	}
	payload, err := readBoundedFile(ctx, file, maxResourceApprovalBytes)
	if err != nil {
		return resourceApproval{}, err
	}
	var approval resourceApproval
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&approval); err != nil {
		return resourceApproval{}, err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || approval.Format != 1 || approval.ResourceID != resourceID ||
		approval.ValidationVersion <= 0 || !filepath.IsAbs(approval.CanonicalPath) || approval.IdentityHash == "" {
		return resourceApproval{}, errors.New("invalid approval record")
	}
	return approval, nil
}

func writeResourceApproval(root *os.Root, directory *os.File, name string, approval resourceApproval) error {
	payload, err := json.Marshal(approval)
	if err != nil || len(payload) > maxResourceApprovalBytes {
		return errors.New("invalid approval payload")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	temporary := ".approval-" + hex.EncodeToString(random[:])
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err := file.Write(payload); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temporary, name); err != nil {
		return err
	}
	return directory.Sync()
}
