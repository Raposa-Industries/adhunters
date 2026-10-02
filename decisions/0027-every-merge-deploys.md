# 0027 · Every merge to main deploys itself

**Decided:** 2 Oct 2026, by the owner: deploys should run "in a way
permissions are accepted automatically", from GitHub on Blacksmith rather
than from their computer, with no approval step.

Until now a person, or Claude on the owner's computer with the owner's typed
go, built main and ran `setup.sh` on each box by hand. Now the `deploy`
workflow does it after every merge to main whose `ci` run passed
(platform/OPERATIONS.md, "Deploys").

- **The merge is the owner's word.** AGENTS.md's "never change production
  without the owner's word" is met by merging to main: whoever merges a PR,
  a person or Claude under the owner's standing ask for green PRs, sends it
  to the boxes. A change that must not go out yet does not merge yet.
- **A box that fails keeps its old build.** Each box checks its services a
  minute after setup.sh and puts the previous build back if one is down or
  restarting; the deploy stops at that box.
- **No key in GitHub that opens the boxes by itself.** The job joins the
  tailnet with a Tailscale OAuth client as `tag:ci`, which the tailnet policy
  lets reach only the boxes' SSH as `admin`, through Tailscale SSH. Removing
  the client or the rule stops every deploy at once.

**Not covered:** settings files (`/etc/adhunters/*.env`) stay the owner's to
write; migrations still only add (AGENTS.md); imports and other one-off jobs
still run by hand.
