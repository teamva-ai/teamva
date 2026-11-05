import { getContext, setContext } from "svelte";
import type {
  Agent, AgentService, Computer, Project, User, UserPreferences,
  UserPreferencesPatch
} from "../lib/types";

export interface ConsoleContext {
  user: User;
  computers: Computer[];
  projects: Project[];
  services: AgentService[];
  agents: Agent[];
  computer?: Computer;
  project?: Project;
  preferences: UserPreferences;
  preferencesSaving: boolean;
  updatePreferences: (patch: UserPreferencesPatch) => Promise<UserPreferences>;
  openProjectCreator: () => void;
  refresh: () => void;
}

const CONSOLE_CONTEXT_KEY = Symbol("console-context");

export function provideConsoleContext(context: ConsoleContext) {
  setContext(CONSOLE_CONTEXT_KEY, context);
}

export function useConsole(): ConsoleContext {
  return getContext<ConsoleContext>(CONSOLE_CONTEXT_KEY);
}

// RequireProject renders children only when a computer and project exist.
export function hasProject(context: ConsoleContext): boolean {
  return Boolean(context.computer && context.project);
}
