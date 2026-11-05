import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { queryClient } from "../lib/query";
import { t } from "../lib/i18n.svelte";
import { cleanWorkCondition, type WorkQuery } from "../lib/workRecords";
import { workTranslations } from "../locales/work";
import ConsolePageTestHost from "../test/ConsolePageTestHost.svelte";
import WorkRecords from "./WorkRecords.svelte";

const record = { id:"op_one",kind:"operation",action:"agent.create",actor_type:"user",actor_id:"user",object_type:"agent",object_id:"agt_one",object_version:1,title:"分析 Agent",project_id:"",run_id:"",parent_id:"",request_id:"req_one",client_id:"client_one",page_id:"page_one",entry_type:"browser",sequence:0,outcome:"succeeded",occurred_at:"2026-09-16T00:00:00Z",received_at:"2026-09-16T00:00:00Z",finished_at:"2026-09-16T00:00:01Z",refs:{agent_id:"agt_one"},before:{},after:{name:"分析 Agent"},error_code:"" };
function mock() {
  const queries: WorkQuery[] = [];const saved: unknown[]=[];const cleared:unknown[]=[];
  vi.stubGlobal("fetch",vi.fn(async (input:string|URL|Request,init?:RequestInit)=>{
    const path=String(input);let data:unknown={items:[]};
    if(path.endsWith("/work-records/query")){queries.push(JSON.parse(String(init?.body)));data={items:[record],total:1};}
    else if(path.endsWith("/work-records/op_one"))data={record,children:[],links:[{kind:"agent",id:"agt_one",title:"分析 Agent",url:"/agents?agent=agt_one",deleted:false}]};
    else if(path.endsWith("/work-record-filters")&&init?.method==="POST"){saved.push(JSON.parse(String(init.body)));data={id:"filter_one"};}
    else if(path.endsWith("/work-records/clear")){cleared.push(JSON.parse(String(init?.body)));data={deleted:1};}
    return new Response(JSON.stringify({data}),{headers:{"content-type":"application/json"}});
  }));return {queries,saved,cleared};
}
afterEach(()=>{cleanup();queryClient.clear();vi.unstubAllGlobals();});
describe("work history",()=>{
  it("queries globally, applies filters, saves and expands inline details",async()=>{
    const calls=mock();render(ConsolePageTestHost,{page:WorkRecords});
    expect(await screen.findByText("分析 Agent")).toBeInTheDocument();
    expect(calls.queries[0].filter).toBeUndefined();
    await fireEvent.input(screen.getByPlaceholderText(t("work.search")),{target:{value:"agt_one"}});
    await fireEvent.click(screen.getByRole("button",{name:t("work.apply")}));
    await waitFor(()=>expect(calls.queries.at(-1)?.search).toBe("agt_one"));
    await fireEvent.input(screen.getByPlaceholderText(t("work.filterName")),{target:{value:"Agent changes"}});
    await fireEvent.click(screen.getByRole("button",{name:t("work.saveFilter")}));
    await waitFor(()=>expect(calls.saved).toEqual([{name:"Agent changes",query:expect.objectContaining({search:"agt_one"})}]));
    await fireEvent.click(screen.getByRole("button",{name:`${t("work.detail")} op_one`}));
    expect(await screen.findByText(t("work.related"))).toBeInTheDocument();
    expect(screen.getByText(t("work.before"))).toBeInTheDocument();
  });
  it("requires confirmation before clearing and supplies a cutoff",async()=>{
    const calls=mock();render(ConsolePageTestHost,{page:WorkRecords});await screen.findByText("分析 Agent");
    await fireEvent.click(screen.getByRole("button",{name:t("work.clear")}));
    expect(calls.cleared).toEqual([]);expect(screen.getByText(t("work.clearHint"))).toBeInTheDocument();
    const buttons=screen.getAllByRole("button",{name:t("work.clear")});await fireEvent.click(buttons.at(-1)!);
    await waitFor(()=>expect(calls.cleared).toEqual([{confirm:true,query:expect.objectContaining({until:expect.any(String)})}]));
  });
  it("normalizes nested groups and supplies all nine complete dictionaries",()=>{
    expect(cleanWorkCondition({join:"and",conditions:[{join:"or",conditions:[{field:"action",operator:"contains",value:"fork"},{field:"title",operator:"eq",value:""}]}]})).toEqual({join:"and",conditions:[{join:"or",conditions:[{field:"action",operator:"contains",value:"fork"}]}]});
    expect(Object.keys(workTranslations)).toHaveLength(9);
    for(const dict of Object.values(workTranslations)){expect(Object.keys(dict)).toEqual(Object.keys(workTranslations["en-US"]));expect(Object.values(dict).every(v=>typeof v==="string"&&v.length>0)).toBe(true);}
  });
});
