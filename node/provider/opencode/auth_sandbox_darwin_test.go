//go:build darwin

package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teamva-ai/teamva/node/provider"
)

func TestMacOSOriginalAuthInsideBusinessRootIsAlwaysReadOnly(t *testing.T) {
	for _, aliasSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct-source", true: "symlink-source"}[aliasSource], func(t *testing.T) {
			isolateNativeFixtureEnvironment(t)
			base := permissionFixtureDirectory(t, t.TempDir())
			root := permissionFixtureDirectory(t, base, "business")
			account := permissionFixtureDirectory(t, root, "original-account", "opencode")
			original := filepath.Join(account, "auth.json")
			writeFile(t, original, `{"fixture":"original-account-marker"}`)
			dataHome := filepath.Dir(account)
			if aliasSource {
				dataHome = permissionFixtureDirectory(t, root, "account-alias-home")
				aliases := permissionFixtureDirectory(t, dataHome, "opencode")
				if err := os.Symlink(original, filepath.Join(aliases, "auth.json")); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("XDG_DATA_HOME", dataHome)
			config := permissionFixtureDirectory(t, base, "instance-config")
			data := permissionFixtureDirectory(t, base, "instance-data")
			adapter, err := newInstance(provider.InstanceOptions{ConfigDir: config, DataDir: data,
				Config: provider.InstanceConfig{WritableRepositories: []string{root}}})
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.Close()
			client := adapter.(*Client)
			if err := client.prepareRunSandbox(t.Context(), provider.RunRequest{CWD: root}); err != nil {
				t.Fatal(err)
			}
			binding := filepath.Join(data, "opencode", "auth.json")
			source := filepath.Join(dataHome, "opencode", "auth.json")
			nativeRules := client.permissionRules(provider.RunRequest{CWD: root})
			rel, _ := filepath.Rel(root, original)
			if permissionEffect(nativeRules, "edit", original) != "deny" || permissionEffect(nativeRules, "edit", rel) != "deny" {
				t.Fatal("native edit rules did not protect an auth target inside an approved root")
			}
			script := `const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const deny = fn => assert.throws(fn, e => e.code === 'EPERM' || e.code === 'EACCES');
const original = ` + quoteJSON(original) + `;
const binding = ` + quoteJSON(binding) + `;
const source = ` + quoteJSON(source) + `;
assert.equal(fs.readFileSync(binding, 'utf8'), '{"fixture":"original-account-marker"}');
deny(() => fs.writeFileSync(binding, 'must-not-write'));
deny(() => fs.writeFileSync(original, 'must-not-write'));
const caseAlias = path.join(path.dirname(original), 'AUTH.JSON');
if (fs.existsSync(caseAlias)) deny(() => fs.writeFileSync(caseAlias, 'must-not-write'));
const replacement = ` + quoteJSON(filepath.Join(root, "auth-replacement")) + `;
fs.writeFileSync(replacement, 'fixture-replacement');
deny(() => fs.renameSync(replacement, original));
deny(() => fs.unlinkSync(original));
deny(() => fs.renameSync(original, original + '.renamed'));
deny(() => fs.renameSync(path.dirname(original), path.dirname(original) + '-renamed'));
deny(() => fs.renameSync(path.dirname(path.dirname(original)), path.dirname(path.dirname(original)) + '-renamed'));
deny(() => fs.writeFileSync(source, 'must-not-write'));
deny(() => fs.unlinkSync(source));
deny(() => fs.renameSync(path.dirname(source), path.dirname(source) + '-renamed'));
deny(() => fs.unlinkSync(binding));
deny(() => fs.renameSync(path.dirname(binding), path.dirname(binding) + '-renamed'));
deny(() => fs.linkSync(original, ` + quoteJSON(filepath.Join(root, "auth-hardlink")) + `));
fs.writeFileSync(` + quoteJSON(filepath.Join(root, "legitimate-coding-marker")) + `, 'allowed');
fs.writeFileSync(` + quoteJSON(filepath.Join(data, "opencode", "native-data-marker")) + `, 'allowed');
`
			cmd, err := client.writeSandbox.command("node", "-e", script)
			if err != nil {
				t.Fatal(err)
			}
			cmd.Dir = root
			cmd.Env = client.writeSandbox.environment([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")})
			if err := cmd.Run(); err != nil {
				t.Fatal("original fake authentication source was not immutable inside its business root")
			}
			payload, err := os.ReadFile(original)
			if err != nil || string(payload) != `{"fixture":"original-account-marker"}` {
				t.Fatal("OS sandbox modified the original fake auth marker")
			}
			if _, err := os.Stat(filepath.Join(root, "legitimate-coding-marker")); err != nil {
				t.Fatal("protecting original auth disabled legitimate business writes")
			}
		})
	}
}
