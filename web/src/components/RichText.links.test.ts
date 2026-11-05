import {cleanup,render,screen} from "@testing-library/svelte";
import {afterEach,expect,it} from "vitest";
import RichText from "./RichText.svelte";
afterEach(cleanup);
it("renders safe assistant citations without interpreting arbitrary HTML or scripts",()=>{
  render(RichText,{text:'[灵感](/inspirations?note=ins_one) [文档](https://example.test) [坏链接](javascript:alert%281%29) <img src=x onerror=alert(1)>',enableLinks:true});
  expect(screen.getByRole("link",{name:"灵感"})).toHaveAttribute("href","/inspirations?note=ins_one");
  expect(screen.getByRole("link",{name:"文档"})).toHaveAttribute("rel","noopener noreferrer");
  expect(screen.getAllByRole("link")).toHaveLength(2);expect(document.querySelector("img")).toBeNull();
});
