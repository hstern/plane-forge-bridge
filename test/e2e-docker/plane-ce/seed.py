"""Bootstrap a Plane CE v1.3.1 instance for the bridge's e2e test.

Runs inside the plane-api container via:
    docker compose exec plane-api python /etc/pfb-seed/seed.py

Creates:
  - the singleton Instance row + InstanceConfiguration entries
  - an admin user (uid=pfb-admin, email=pfb-admin@pfb.test) with a known password
  - a Workspace ("pfb-ci", slug=pfb-ci)
  - a Project ("PFB", identifier="PFB")
  - an APIToken with a known value (PFB_PLANE_API_KEY env var, falls back to a generated one)

Bypasses the /auth/sign-up/ HTML flow (session cookies, CSRF, redirects) and
the UI workspace onboarding flow — both are expensive to drive from a script
and exist for human operators, not bridge bootstrap. Inserting directly
through Django ORM keeps the seed self-contained and idempotent: re-running
the seed is a no-op once everything exists.

Emits the API token to stdout (last line); the surrounding shell script
captures it for the bridge config + integration test env.
"""
import os
import sys
import uuid

import django

os.environ.setdefault("DJANGO_SETTINGS_MODULE", "plane.settings.production")
django.setup()

from django.contrib.auth.hashers import make_password
from django.utils import timezone

from plane.db.models import (
    APIToken,
    Project,
    ProjectIdentifier,
    ProjectMember,
    User,
    Workspace,
    WorkspaceMember,
)
from plane.license.models import Instance, InstanceAdmin, InstanceEdition


ADMIN_EMAIL = "pfb-admin@pfb.test"
ADMIN_PASSWORD = "pfb-admin-pass-ci-only"
ADMIN_USERNAME = "pfb-admin"
WORKSPACE_NAME = "pfb-ci"
WORKSPACE_SLUG = "pfb-ci"
PROJECT_NAME = "PFB Bridge E2E"
PROJECT_IDENTIFIER = "PFB"
TOKEN_LABEL = "pfb-bridge-e2e"


def ensure_instance() -> Instance:
    instance = Instance.objects.first()
    if instance is None:
        instance = Instance.objects.create(
            instance_name="Plane CE - pfb e2e",
            instance_id=uuid.uuid4().hex[:24],
            current_version=os.environ.get("APP_VERSION", "v1.3.1"),
            latest_version=os.environ.get("APP_VERSION", "v1.3.1"),
            last_checked_at=timezone.now(),
            is_test=True,
            edition=InstanceEdition.PLANE_COMMUNITY.value,
        )
    if not instance.is_setup_done:
        instance.is_setup_done = True
        instance.save(update_fields=["is_setup_done"])
    return instance


def ensure_admin_user() -> User:
    user, created = User.objects.get_or_create(
        email=ADMIN_EMAIL,
        defaults={
            "username": ADMIN_USERNAME,
            "password": make_password(ADMIN_PASSWORD),
            "is_active": True,
            "is_email_verified": True,
            "first_name": "PFB",
            "last_name": "Admin",
            "display_name": ADMIN_USERNAME,
        },
    )
    if created:
        # Newly created users land without the password hash bit being
        # marked "set" — fix that up so downstream auth flows work.
        user.set_password(ADMIN_PASSWORD)
        user.is_password_autoset = False
        user.save()
    return user


def ensure_instance_admin(instance: Instance, user: User) -> None:
    InstanceAdmin.objects.get_or_create(
        instance=instance,
        user=user,
        defaults={"role": 20},  # ROLE = Owner; see plane.license.models
    )


def ensure_workspace(owner: User) -> Workspace:
    workspace, created = Workspace.objects.get_or_create(
        slug=WORKSPACE_SLUG,
        defaults={
            "name": WORKSPACE_NAME,
            "owner": owner,
            "organization_size": "Just myself",
        },
    )
    WorkspaceMember.objects.get_or_create(
        workspace=workspace,
        member=owner,
        defaults={"role": 20},  # Admin
    )
    return workspace


def ensure_project(workspace: Workspace, owner: User) -> Project:
    project, created = Project.objects.get_or_create(
        workspace=workspace,
        identifier=PROJECT_IDENTIFIER,
        defaults={
            "name": PROJECT_NAME,
            "created_by": owner,
            "updated_by": owner,
            "network": 2,  # workspace-public
        },
    )
    ProjectIdentifier.objects.get_or_create(
        workspace=workspace,
        name=PROJECT_IDENTIFIER,
        defaults={"project": project},
    )
    ProjectMember.objects.get_or_create(
        workspace=workspace,
        project=project,
        member=owner,
        defaults={"role": 20, "is_active": True},
    )
    return project


def ensure_default_states(project: Project, owner: User) -> None:
    """Plane normally seeds default states (Backlog, Todo, In Progress,
    Done, Cancelled) on workspace/project creation via a signal. We
    create them explicitly so the bridge's state-map lookups have
    something to find."""
    from plane.db.models import State

    defaults = [
        ("Backlog", "backlog", "#5e6ad2"),
        ("Todo", "unstarted", "#3f76ff"),
        ("In Progress", "started", "#f59e0b"),
        ("Done", "completed", "#22c55e"),
        ("Cancelled", "cancelled", "#ef4444"),
    ]
    for name, group, color in defaults:
        State.objects.get_or_create(
            workspace=project.workspace,
            project=project,
            name=name,
            defaults={
                "group": group,
                "color": color,
                "default": name == "Backlog",
                "created_by": owner,
                "updated_by": owner,
            },
        )


def ensure_api_token(user: User, workspace: Workspace) -> APIToken:
    desired_token = os.environ.get("PFB_PLANE_API_KEY", "").strip()
    existing = APIToken.objects.filter(
        user=user, workspace=workspace, label=TOKEN_LABEL
    ).first()
    if existing:
        if desired_token and existing.token != desired_token:
            existing.token = desired_token
            existing.save(update_fields=["token"])
        return existing
    return APIToken.objects.create(
        user=user,
        workspace=workspace,
        label=TOKEN_LABEL,
        description="bridge e2e token (CI only)",
        token=desired_token or uuid.uuid4().hex,
        user_type=0,
        is_active=True,
        is_service=False,
        allowed_rate_limit="600/minute",
    )


def main() -> None:
    instance = ensure_instance()
    user = ensure_admin_user()
    ensure_instance_admin(instance, user)
    workspace = ensure_workspace(user)
    project = ensure_project(workspace, user)
    ensure_default_states(project, user)
    token = ensure_api_token(user, workspace)

    sys.stderr.write(
        f"seed: instance={instance.instance_id} user={user.email} "
        f"workspace_slug={workspace.slug} workspace_id={workspace.id} "
        f"project_id={project.id} project_identifier={project.identifier}\n"
    )
    # stdout: a single JSON line with everything the caller needs to wire
    # the bridge config. Captured by seed.sh.
    import json
    print(json.dumps({
        "workspace_slug": workspace.slug,
        "workspace_id": str(workspace.id),
        "project_id": str(project.id),
        "project_identifier": project.identifier,
        "admin_user_id": str(user.id),
        "admin_email": user.email,
        "api_token": token.token,
    }))


if __name__ == "__main__":
    main()
