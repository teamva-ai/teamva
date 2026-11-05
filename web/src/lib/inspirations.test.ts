import { describe,expect,it,vi } from "vitest";
import { InspirationSaveQueue,noteDraft,type Inspiration } from "./inspirations";
import { inspirationTranslations } from "../locales/inspirations";
import { APIError } from "./api";

const note:Inspiration={id:"ins_one",version:1,created_at:"2026-09-16T00:00:00Z",updated_at:"2026-09-16T00:00:00Z",project_id:"",title:"Idea",markdown:"# Initial",completed:false,completed_at:null};
describe("inspiration saving",()=>{
  it("serializes saves and preserves edits made during an outstanding request",async()=>{
    let current=noteDraft({...note,markdown:"# First"});let release!:(n:Inspiration)=>void;
    const write=vi.fn().mockImplementationOnce(()=>new Promise<Inspiration>(resolve=>{release=resolve;})).mockImplementationOnce(async(_id,version,draft)=>({...note,...draft,version:version+1}));
    const queue=new InspirationSaveQueue(note.id,note,write,()=>{});
    const first=queue.flush(()=>current);const second=queue.flush(()=>current);
    expect(first).toBe(second);current={...current,markdown:"# Second"};release({...note,markdown:"# First",version:2});await first;
    expect(write).toHaveBeenNthCalledWith(1,note.id,1,expect.objectContaining({markdown:"# First"}));expect(write).toHaveBeenNthCalledWith(2,note.id,2,expect.objectContaining({markdown:"# Second"}));expect(queue.dirty(current)).toBe(false);
  });
  it("keeps a Markdown draft after failure and retries with the same version",async()=>{
    const current=noteDraft({...note,markdown:"**Keep me**"});const write=vi.fn().mockRejectedValueOnce(new Error("conflict")).mockResolvedValueOnce({...note,...current,version:2});const queue=new InspirationSaveQueue(note.id,note,write,()=>{});
    await expect(queue.flush(()=>current)).rejects.toThrow("conflict");expect(queue.dirty(current)).toBe(true);await queue.flush(()=>current);expect(write).toHaveBeenLastCalledWith(note.id,1,{markdown:current.markdown,project_id:current.project_id,completed:current.completed});
  });
  it("does not rewrite untouched Markdown or provide editor JSON storage",async()=>{
    const write=vi.fn();const queue=new InspirationSaveQueue(note.id,note,write,()=>{});await queue.flush(()=>noteDraft(note));expect(write).not.toHaveBeenCalled();expect(Object.keys(noteDraft(note))).toEqual(["title","markdown","project_id","completed"]);
    expect(Object.keys(inspirationTranslations)).toHaveLength(9);for(const dictionary of Object.values(inspirationTranslations))expect(Object.values(dictionary).every(v=>typeof v==="string"&&v.length>0)).toBe(true);
  });
  it("adopts an asynchronous title/version without saving an old default over it", async () => {
    let current = { ...noteDraft(note), titleRevision: 0, markdown: "Unsaved body" };
    const write = vi.fn(async (_id, version, patch) => ({ ...note, title: "Automatic title", ...patch, version: version + 1 }));
    const queue = new InspirationSaveQueue(note.id, note, write, (saved, submitted) => {
      if (current.titleRevision === submitted.titleRevision) current = { ...current, title: saved.title };
    });
    expect(queue.acceptServer({ ...note, title: "Automatic title", version: 2 })).toBe(true);
    expect(current).toMatchObject({ title: "Automatic title", markdown: "Unsaved body" });
    await queue.flush(() => current);
    expect(write).toHaveBeenCalledExactlyOnceWith(note.id, 2, { markdown: "Unsaved body", completed: false, project_id: "" });
    expect(queue.dirty(current)).toBe(false);
  });
  it("keeps explicit title intent even after typing back to the baseline title", async () => {
    const current = { ...noteDraft(note), titleRevision: 2 };
    const write = vi.fn(async (_id, version, patch) => ({ ...note, ...patch, version: version + 1 }));
    const queue = new InspirationSaveQueue(note.id, note, write, () => {});
    expect(queue.dirty(current)).toBe(true);
    await queue.flush(() => current);
    expect(write).toHaveBeenCalledExactlyOnceWith(note.id, 1, expect.objectContaining({ title: note.title }));
    expect(queue.dirty(current)).toBe(false);
  });
  it("rebases one 409 caused only by asynchronous naming, retaining later typing and a manual title", async () => {
    let current = { ...noteDraft(note), titleRevision: 0, markdown: "First body" };
    let reject!: (error: Error) => void;
    const write = vi.fn().mockImplementationOnce(() => new Promise<Inspiration>((_resolve, fail) => { reject = fail; }))
      .mockImplementation(async (_id, version, patch) => ({ ...note, title: "Automatic title", ...patch, version: version + 1 }));
    const reload = vi.fn(async () => ({ ...note, title: "Automatic title", version: 2 }));
    const queue = new InspirationSaveQueue(note.id, note, write, (saved, submitted) => {
      if (current.titleRevision === submitted.titleRevision) current = { ...current, title: saved.title };
    }, reload);
    const flushing = queue.flush(() => current);
    current = { ...current, title: "My title", titleRevision: 1, markdown: "Later body" };
    reject(new APIError(409, { error: { code: "version_conflict" } }));
    await flushing;
    expect(write).toHaveBeenNthCalledWith(2, note.id, 2, { markdown: "First body", completed: false, project_id: "" });
    expect(write).toHaveBeenNthCalledWith(3, note.id, 3, { title: "My title", markdown: "Later body", completed: false, project_id: "" });
    expect(current).toMatchObject({ title: "My title", markdown: "Later body" });
    expect(queue.dirty(current)).toBe(false);
  });
  it("does not rebase a real concurrent body edit or lose its local draft", async () => {
    const current = { ...noteDraft(note), titleRevision: 0, markdown: "Keep this draft" };
    const conflict = new APIError(409, { error: { code: "version_conflict" } });
    const write = vi.fn().mockRejectedValue(conflict);
    const queue = new InspirationSaveQueue(note.id, note, write, () => {}, async () => ({ ...note, markdown: "Remote body", version: 2 }));
    expect(queue.acceptServer({ ...note, markdown: "Remote body", version: 2 })).toBe(false);
    await expect(queue.flush(() => current)).rejects.toBe(conflict);
    expect(write).toHaveBeenCalledTimes(1);
    expect(queue.dirty(current)).toBe(true);
    expect(current.markdown).toBe("Keep this draft");
  });
  it("ignores wrong-note or older polls and stops draining after its editor unmounts", async () => {
    let current = { ...noteDraft(note), titleRevision: 0, markdown: "Submitted" };
    let release!: (saved: Inspiration) => void;
    const write = vi.fn(() => new Promise<Inspiration>((resolve) => { release = resolve; }));
    const onSaved = vi.fn();
    const queue = new InspirationSaveQueue(note.id, note, write, onSaved);
    expect(queue.acceptServer({ ...note, id: "other", version: 4 })).toBe(false);
    expect(queue.acceptServer({ ...note, version: 1 })).toBe(false);
    const flushing = queue.flush(() => current);
    current = { ...current, markdown: "Not for the next editor" };
    queue.stop();
    release({ ...note, markdown: "Submitted", version: 2 });
    await flushing;
    expect(write).toHaveBeenCalledTimes(1);
    expect(onSaved).not.toHaveBeenCalled();
  });
});
