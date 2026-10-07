#!/usr/bin/env python3
"""Verify konflux-*-bot-actions ClusterRoles are a subset of konflux-admin-user-actions.

Namespace admins bind the bot ClusterRoles to their konflux-bot-* ServiceAccounts.
The apiserver's privilege-escalation check refuses a RoleBinding unless the creator
already holds every permission in the referenced role, so any permission that lands
in a bot role but not in konflux-admin-user-actions silently breaks that workflow:

  rolebindings.rbac.authorization.k8s.io "..." is forbidden: user "..." is
  attempting to grant RBAC permissions not currently held: {...}

Granting admins the bind verb would waive that check, but bind is not subject-scoped
-- the restrict-bindings-serviceaccounts VAP only constrains konflux-bot-* subjects,
so an admin could bind a bot role to themselves and pick up anything the admin role
lacks. Keeping the bot roles a subset avoids needing bind at all.

Scope: components/konflux-rbac/<env>/<cluster>/ overlays, which ship both the bot
roles and konflux-admin-user-actions and are therefore self-contained.

Operator-owned clusters are deliberately NOT checked. They are excluded from the
konflux-rbac ApplicationSet (argo-cd-apps/overlays/*/exclude-operator-owned-clusters.yaml)
and konflux-operator supplies their roles instead. There its konflux-admin-user-actions
is an aggregated ClusterRole whose sources carry the label
rbac.konflux-ci.dev/aggregate-to-admin and live in the operator's own manifests, not
this repo -- so its effective permissions cannot be determined from here, by proxy or
by resolving the aggregation. A check against a stand-in admin role would pass roles
that are not actually bindable, which is worse than no check. Verifying those clusters
belongs in konflux-ci/operator.

Which roles are in scope is read at runtime from the restrict-binding-serviceaccounts
ValidatingAdmissionPolicies, which decide what a konflux-bot-* ServiceAccount may be
bound to. The pattern is not duplicated here, so the guard cannot drift from the
policy; disagreement between policy copies is itself an error. Today that pattern is
^konflux-.+-bot-actions$, which konflux-tester-internalbot-actions does not match.

This guard fails closed. Rule shapes it cannot faithfully model -- nonResourceURLs,
aggregationRule -- are reported as errors rather than skipped, because silently
ignoring a rule the apiserver does evaluate would let a real violation through.

Usage:
    hack/verify-bot-clusterroles-subset.py <dir> [<dir> ...]

Exits non-zero and lists the offending permissions if any bot role exceeds the
admin role. Set OUTPUT=GITHUB to emit GitHub Actions error annotations.
"""

import argparse
import os
import pathlib
import re
import subprocess
import sys

import yaml

ADMIN_ROLE = "konflux-admin-user-actions"

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent
# Every copy of the restrict-binding-serviceaccounts ValidatingAdmissionPolicy: the
# shared base plus one per policy ring. The role-name pattern is read from these
# rather than hardcoded here, so the guard cannot drift from the policy it mirrors.
POLICY_GLOB = ("components/policies/**/restrict-binding-serviceaccounts/*/"
               "*validatingadmissionpolicy.yaml")
POLICY_PATTERN_VAR = "allowedClusterRoleNamePattern"


def _cel_string_literal(expression):
    """Unwrap a CEL string literal such as "'^konflux-.+-bot-actions$'"."""
    text = (expression or "").strip()
    if len(text) >= 2 and text[0] == text[-1] and text[0] in ("'", '"'):
        return text[1:-1]
    raise RuntimeError(
        f"{POLICY_PATTERN_VAR} is {expression!r}, which is not a plain CEL string "
        f"literal; this guard can only derive its scope from a literal pattern"
    )


def bot_role_pattern():
    """Read the bot-role name pattern from the admission policies.

    The policy decides which ClusterRoles a konflux-bot-* ServiceAccount may be bound
    to; roles outside it are never bound to bot ServiceAccounts, so admins never need
    to hold their rules. Reading it here keeps the two definitions in lockstep, and
    disagreement between policy copies is itself an error worth failing on.
    """
    found = {}
    for path in sorted(REPO_ROOT.glob(POLICY_GLOB)):
        if ".chainsaw-test" in path.parts:
            continue
        with open(path) as handle:
            doc = yaml.safe_load(handle)
        if not doc or doc.get("kind") != "ValidatingAdmissionPolicy":
            continue
        for variable in (doc.get("spec") or {}).get("variables") or []:
            if variable.get("name") == POLICY_PATTERN_VAR:
                rel = path.relative_to(REPO_ROOT)
                found[str(rel)] = _cel_string_literal(variable.get("expression"))

    if not found:
        raise RuntimeError(
            f"no ValidatingAdmissionPolicy defining {POLICY_PATTERN_VAR} found under "
            f"{POLICY_GLOB}; cannot determine which ClusterRoles are in scope"
        )

    distinct = set(found.values())
    if len(distinct) > 1:
        detail = "; ".join(f"{p}: {v!r}" for p, v in sorted(found.items()))
        raise RuntimeError(
            f"admission policies disagree on {POLICY_PATTERN_VAR} ({detail}); "
            f"reconcile them before this guard can determine its scope"
        )

    pattern = distinct.pop()
    try:
        return re.compile(pattern), pattern, len(found)
    except re.error as exc:
        raise RuntimeError(f"{POLICY_PATTERN_VAR} {pattern!r} is not a valid regex: {exc}")


def render(path):
    """Return {clusterrole name: body} for a kustomize overlay or component dir."""
    result = subprocess.run(
        ["kustomize", "build", "--enable-helm", path],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(f"kustomize build failed for {path}:\n{result.stderr}")
    roles = {}
    for doc in yaml.safe_load_all(result.stdout):
        if not doc or doc.get("kind") != "ClusterRole":
            continue
        name = doc["metadata"]["name"]
        if name in roles:
            raise RuntimeError(
                f"{path}: duplicate ClusterRole {name}; cannot determine which "
                f"definition applies"
            )
        roles[name] = doc
    return roles


def unsupported_rules(role):
    """Return reasons this role cannot be compared faithfully, if any."""
    reasons = []
    if role.get("aggregationRule"):
        reasons.append(
            "has an aggregationRule, so its effective rules are filled in by the "
            "controller at runtime and are not visible here"
        )
    if any(rule.get("nonResourceURLs") for rule in role.get("rules") or []):
        reasons.append(
            "has a nonResourceURLs rule, which the escalation check evaluates but "
            "this comparison does not model"
        )
    return reasons


def split_rules(rules):
    """Split rules into (unscoped, scoped) permission tuples.

    unscoped: (apiGroup, resource, verb)
    scoped:   (apiGroup, resource, verb, frozenset(resourceNames))

    A scoped rule grants strictly less than the same rule unscoped, so the two are
    tracked separately rather than collapsed.
    """
    unscoped, scoped = set(), set()
    for rule in rules or []:
        if rule.get("nonResourceURLs"):
            continue  # reported separately by unsupported_rules()
        names = rule.get("resourceNames")
        for group in rule.get("apiGroups", []):
            for resource in rule.get("resources", []):
                for verb in rule.get("verbs", []):
                    if names:
                        scoped.add((group, resource, verb, frozenset(names)))
                    else:
                        unscoped.add((group, resource, verb))
    return unscoped, scoped


def _matches(holder, group, resource, verb):
    hgroup, hresource, hverb = holder[0], holder[1], holder[2]
    return (hgroup in (group, "*")
            and hresource in (resource, "*")
            and hverb in (verb, "*"))


def covered_by(admin_unscoped, admin_scoped, perm):
    """Does the admin role hold this bot permission?"""
    group, resource, verb = perm[0], perm[1], perm[2]
    names = perm[3] if len(perm) == 4 else None

    # An unscoped admin rule covers the permission whether or not it is scoped.
    if any(_matches(h, group, resource, verb) for h in admin_unscoped):
        return True

    # A scoped bot permission is also covered if the admin's scoped rules together
    # name every object the bot rule names. An unscoped bot permission is never
    # covered by scoped admin rules.
    if names is not None:
        held = set()
        for holder in admin_scoped:
            if _matches(holder, group, resource, verb):
                held |= holder[3]
        if names <= held:
            return True

    return False


def describe(perm):
    group, resource, verb = perm[0], perm[1], perm[2]
    base = f"{verb} {resource}.{group or 'core'}"
    if len(perm) == 4:
        base += f" [resourceNames: {','.join(sorted(perm[3]))}]"
    return base


def check(target, admin_unscoped, admin_scoped, admin_source, github, pattern):
    """Report bot roles in target exceeding the admin role. Returns failure count."""
    roles = render(target)
    failures = 0
    via = "" if admin_source == target else f" (compared against {admin_source})"

    def report(msg):
        if github:
            print(f"::error title=Bot ClusterRole exceeds admin role::{msg}")
        else:
            print(f"ERROR: {msg}", file=sys.stderr)

    for name in sorted(roles):
        if not pattern.match(name):
            continue
        role = roles[name]

        for reason in unsupported_rules(role):
            failures += 1
            report(f"{target}: {name} {reason}. This guard cannot verify it is a "
                   f"subset of {ADMIN_ROLE}; model the rule here or remove it.")

        bot_unscoped, bot_scoped = split_rules(role.get("rules"))
        excess = [p for p in sorted(bot_unscoped) if not covered_by(admin_unscoped, admin_scoped, p)]
        excess += [p for p in sorted(bot_scoped) if not covered_by(admin_unscoped, admin_scoped, p)]
        if not excess:
            continue
        failures += 1
        detail = ", ".join(describe(p) for p in excess)
        report(
            f"{target}: {name} grants {len(excess)} permission(s) not held by "
            f"{ADMIN_ROLE}{via}: {detail}. Admins will not be able to bind this "
            f"role to konflux-bot-* ServiceAccounts. Add the permission to "
            f"{ADMIN_ROLE} or remove it from the bot role."
        )
    return failures


def admin_perms_from(path):
    roles = render(path)
    if ADMIN_ROLE not in roles:
        return None
    role = roles[ADMIN_ROLE]
    reasons = unsupported_rules(role)
    if reasons:
        raise RuntimeError(
            f"{path}: {ADMIN_ROLE} {reasons[0]}; cannot use it as a comparison basis"
        )
    return split_rules(role.get("rules"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("targets", nargs="+", help="kustomize overlay or component dirs")
    args = parser.parse_args()
    github = os.environ.get("OUTPUT") == "GITHUB"

    pattern, pattern_text, policy_count = bot_role_pattern()
    print(f"Scope: ClusterRoles matching {pattern_text!r} "
          f"(from {policy_count} admission policy file(s))")

    failures = 0
    for target in args.targets:
        perms = admin_perms_from(target)
        if perms is None:
            print(f"ERROR: {target} defines no {ADMIN_ROLE}", file=sys.stderr)
            return 2
        admin_unscoped, admin_scoped = perms
        failures += check(target, admin_unscoped, admin_scoped, target, github, pattern)

    if failures:
        return 1
    print(f"OK: all bot ClusterRoles are a subset of {ADMIN_ROLE} "
          f"across {len(args.targets)} target(s)")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except RuntimeError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(2)
