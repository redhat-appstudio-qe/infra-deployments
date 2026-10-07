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

Two sources ship these roles. components/konflux-rbac/<env>/<cluster>/ serves the
clusters Argo CD targets directly; operator-owned clusters are excluded from that
ApplicationSet (see argo-cd-apps/overlays/*/exclude-operator-owned-clusters.yaml)
and get theirs from components/konflux-operator/rings/*/base/cr/konflux-rbac/
instead. Both must satisfy the invariant, so both are checked.

Which roles are in scope is decided with the same regex the VAP uses to decide which
ClusterRoles may be bound to a konflux-bot-* ServiceAccount, so the two cannot drift.
konflux-tester-internalbot-actions does not match it and is therefore not checked.

This guard fails closed. Rule shapes it cannot faithfully model -- nonResourceURLs,
aggregationRule -- are reported as errors rather than skipped, because silently
ignoring a rule the apiserver does evaluate would let a real violation through.

Usage:
    hack/verify-bot-clusterroles-subset.py <dir> [<dir> ...]
    hack/verify-bot-clusterroles-subset.py --admin-from <dir> <dir> [<dir> ...]

--admin-from names a directory to source konflux-admin-user-actions from, for
targets that ship bot roles without an admin role. The konflux-operator CR
directories are such targets: on those clusters the admin role is supplied by the
konflux-operator image rather than this repo, so the in-repo role is used as a
stand-in. That is a proxy, not a guarantee -- see the note in the workflow.

Exits non-zero and lists the offending permissions if any bot role exceeds the
admin role. Set OUTPUT=GITHUB to emit GitHub Actions error annotations.
"""

import argparse
import os
import re
import subprocess
import sys

import yaml

ADMIN_ROLE = "konflux-admin-user-actions"
# The pattern restrict-bindings-serviceaccounts-create.konflux-ci.dev uses to decide
# which ClusterRoles a konflux-bot-* ServiceAccount may be bound to. Roles outside it
# are never bound to bot ServiceAccounts, so admins never need to hold their rules.
BOT_ROLE_PATTERN = re.compile(r"^konflux-.+-bot-actions$")


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


def check(target, admin_unscoped, admin_scoped, admin_source, github):
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
        if not BOT_ROLE_PATTERN.match(name):
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
    parser.add_argument("--admin-from", metavar="DIR",
                        help=f"source {ADMIN_ROLE} from DIR instead of from each target")
    args = parser.parse_args()
    github = os.environ.get("OUTPUT") == "GITHUB"

    shared = None
    if args.admin_from:
        shared = admin_perms_from(args.admin_from)
        if shared is None:
            print(f"ERROR: {args.admin_from} does not define {ADMIN_ROLE}", file=sys.stderr)
            return 2

    failures = 0
    for target in args.targets:
        if shared is not None:
            (admin_unscoped, admin_scoped), source = shared, args.admin_from
        else:
            perms = admin_perms_from(target)
            if perms is None:
                print(f"ERROR: {target} defines no {ADMIN_ROLE}; pass --admin-from",
                      file=sys.stderr)
                return 2
            (admin_unscoped, admin_scoped), source = perms, target
        failures += check(target, admin_unscoped, admin_scoped, source, github)

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
