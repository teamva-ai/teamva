import fs from "node:fs/promises";
import path from "node:path";

// V2.0.18 FileAccess uses lexical paths, not realpaths. Native ordered rules
// enforce the host roots; this deny-only hook closes stable symlink/hardlink
// aliases that would otherwise have an allowed lexical resource. This is NOT
// an OS sandbox and cannot prevent a concurrent external filesystem actor from
// replacing directories between permission evaluation and the native write.
export default {
  id: "teamva.permission-boundary",
  async setup(ctx) {
    const noTools = ctx.options.disableTools === true;
    const instructions = ctx.options.instructions ?? [];
    const boundSkills = new Map((ctx.options.boundSkills ?? []).map((skill) => [skill.id, skill.path]));
    // V2 sources are additive and include HOME compatibility/built-in Skills.
    // Filter the native registry itself (not just autoinvoke or its prompt),
    // replaying after every source refresh. Exact paths also reject ID shadows.
    const filterSkills = (editor) => {
      for (const skill of editor.list()) {
        if (boundSkills.get(skill.id) !== skill.path) editor.remove(skill.id);
      }
    };
    let skillFilter = await ctx.skill.transform(filterSkills);
    // Pinned V2 loads config/compatibility source transforms AFTER user plugin
    // setup. RPC dispatch awaits *all* native plugin activation. The host seals
    // our deny-only filter last before every Run, then verifies /api/skill.
    // Subsequent source refreshes replay their earlier transforms before ours.
    await ctx.rpc.register({
      id: "teamva.skill-bindings",
      methods: { seal: { input: { type: "object", additionalProperties: false }, output: { type: "boolean" } } },
      events: {},
    }, {
      seal: async () => {
        await skillFilter.dispose();
        skillFilter = await ctx.skill.transform(filterSkills);
        return true;
      },
    });
    const runTemp = process.env.OPENCODE_TEAMVA_RUN_TMP;
    if (!noTools && runTemp) {
      const ancestors = [];
      for (let directory = runTemp;; directory = path.dirname(directory)) {
        const original = await fs.lstat(directory);
        if (!original.isDirectory() || original.isSymbolicLink()) {
          throw new Error("Teamva IPC temporary directory is unsafe");
        }
        ancestors.push({ directory, dev: original.dev, ino: original.ino });
        if (path.dirname(directory) === directory) break;
      }
      if (await fs.realpath(runTemp) !== runTemp) throw new Error("Teamva IPC temporary directory is unsafe");
      // V2.0.18 Shell.create consumes process.env or a session environment, then
      // shell.create.before, then spawns invocation.env. Do not guess a V1
      // shellEnvironment config field or allow a saved session env to keep the
      // previous Run's TMPPREFIX. This hook changes only the four temp keys.
      await ctx.shell.hook("create.before", async (event) => {
        for (const original of ancestors) {
          const current = await fs.lstat(original.directory);
          if (!current.isDirectory() || current.isSymbolicLink() || current.dev !== original.dev || current.ino !== original.ino || (original.directory === runTemp && (current.mode & 0o777) !== 0o700)) {
            throw new Error("Teamva IPC temporary directory changed");
          }
        }
        if (await fs.realpath(runTemp) !== runTemp) throw new Error("Teamva IPC temporary directory changed");
        event.env.TMPDIR = runTemp;
        event.env.TMP = runTemp;
        event.env.TEMP = runTemp;
        event.env.TMPPREFIX = path.join(runTemp, "zsh");
      });
    }
    if (noTools) {
      // Replay removal after native/MCP registry refreshes too. Permissions also
      // remove the synthesized Code Mode executor, which is not in this registry.
      await ctx.tool.transform((editor) => {
        for (const tool of editor.list()) editor.remove(tool.id);
      });
    }
    await ctx.session.hook("context", async (event) => {
      if (noTools) {
        // A cold Resume can retain the initial instruction snapshot from a
        // formerly tool-enabled turn. Keep the real agent.system override, not
        // the stale coding/Code Mode catalog embedded in that historical system
        // snapshot. Explicit host workspace/Skill text is reattached below.
        event.system.splice(1);
      } else {
        // Native cold Resume keeps the old initial instruction snapshot. Its
        // Skill catalog must not advertise a previous binding after a rebind,
        // or stay absent after a NoTools turn. Preserve the coding/tool system
        // and user transcript; replace only the native catalog in extras.
        for (const part of event.system.slice(1)) {
          if (part.type === "text") part.text = part.text.replace(/<available_skills>[\s\S]*?<\/available_skills>/g, "");
        }
        const skills = (await ctx.skill.list()).data.filter((skill) => boundSkills.get(skill.id) === skill.path && skill.description !== undefined && skill.autoinvoke !== false);
        const catalog = skills.length === 0 ? "No skills are currently available." : [
          "<available_skills>",
          ...skills.map((skill) => `  <skill>\n    <id>${skill.id}</id>\n    <name>${skill.name}</name>\n    <description>${skill.description}</description>\n  </skill>`),
          "</available_skills>",
        ].join("\n");
        event.system.push({ type: "text", text: "Only the following currently host-bound Skills are available; this catalog supersedes historical Skill guidance.\n" + catalog });
      }
      for (const text of instructions) event.system.push({ type: "text", text });
      if (noTools) {
        // Defense in depth: only this request's executable snapshot is used by
        // V2, so even an unsolicited model tool_call cannot regain a tool.
        for (const name of Object.keys(event.tools)) delete event.tools[name];
      }
    });
    // Changing the Location changes the meaning of relative permission rules.
    // This built-in Code Mode tool does not perform a permission check in V2.
    await ctx.tool.transform((editor) => editor.remove("opencode_session_move"));
    await ctx.permission.hook("evaluate", async (event) => {
      if (noTools) {
        event.effect = "deny";
        event.message = "Teamva disables all tools for this instance";
        return;
      }
      if (event.action === "skill") {
        const current = new Map((await ctx.skill.list()).data.map((skill) => [skill.id, skill.path]));
        if (event.resources.some((id) => !boundSkills.has(id) || boundSkills.get(id) !== current.get(id))) {
          event.effect = "deny";
          event.message = "Teamva blocks unbound Skills";
          return;
        }
      }
      if (event.action !== "edit") return;
      for (const resource of event.resources) {
        const absolute = path.resolve(ctx.location.directory, resource);
        let existing = absolute;
        const suffix = [];
        try {
          for (;;) {
            try {
              const info = await fs.lstat(existing);
              if (info.isSymbolicLink() || (existing === absolute && info.nlink > 1 && info.isFile())) {
                throw new Error("aliased mutation target");
              }
              const canonical = await fs.realpath(existing);
              if (path.resolve(canonical, ...suffix) !== absolute) throw new Error("aliased mutation target");
              break;
            } catch (error) {
              if (error.code !== "ENOENT") throw error;
              const parent = path.dirname(existing);
              if (parent === existing) throw error;
              suffix.unshift(path.basename(existing));
              existing = parent;
            }
          }
        } catch {
          event.effect = "deny";
          event.message = "Teamva blocks aliased or unverifiable mutation paths";
          return;
        }
      }
    });
  },
};
