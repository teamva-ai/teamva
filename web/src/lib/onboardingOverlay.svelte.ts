// The first-run guide is a modal overlay above the workbench, not a route.
// `requested` carries an explicit open (banner / settings rerun / /onboarding
// alias); the derived "needed" state from the server auto-opens it on the
// default entry point.
export const onboardingOverlay = $state({ requested: false });

export function requestOnboarding() {
  onboardingOverlay.requested = true;
}

export function clearOnboardingRequest() {
  onboardingOverlay.requested = false;
}
