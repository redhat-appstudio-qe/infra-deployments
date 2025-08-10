#!/usr/bin/env python3
"""
Tekton-Kueue Configuration Test

A comprehensive test suite that validates the CEL expressions in the tekton-kueue configuration by:

1. **Reading configuration dynamically** from `components/kueue/development/tekton-kueue/config.yaml`
2. **Getting the image** from `components/kueue/staging/base/tekton-kueue/kustomization.yaml`
3. **Running mutations** using the actual tekton-kueue container via podman
4. **Validating results** against expected annotations, labels, and priority classes

Usage:
    # Check if all prerequisites are met
    python hack/test-tekton-kueue-config.py --check-setup

    # Run all tests
    python hack/test-tekton-kueue-config.py

    # Run tests with verbose output
    python hack/test-tekton-kueue-config.py --verbose

Test Scenarios:
    The test covers all CEL expressions in the configuration:

    1. **Multi-platform Resource Requests**:
       - New style: `build-platforms` parameter → `kueue.konflux-ci.dev/requests-*` annotations
       - Old style: `PLATFORM` parameters in tasks → `kueue.konflux-ci.dev/requests-*` annotations

    2. **Priority Assignment Logic**:
       - Push events → `konflux-post-merge-build`
       - Pull requests → `konflux-pre-merge-build` 
       - Integration test push → `konflux-post-merge-test`
       - Integration test PR → `konflux-pre-merge-test`
       - Release managed → `konflux-release`
       - Release tenant → `konflux-tenant-release`
       - Mintmaker namespace → `konflux-dependency-update`
       - Default → `konflux-default`

    3. **Queue Assignment**: All PipelineRuns get `kueue.x-k8s.io/queue-name: pipelines-queue`

Prerequisites:
    - Python 3 with PyYAML
    - Podman (for running the tekton-kueue container)
    - Access to the tekton-kueue image specified in the kustomization

CI/CD Integration:
    The test runs automatically on pull requests via the GitHub action 
    `.github/workflows/test-tekton-kueue-config.yaml` when:
    - Changes are made to `components/kueue/**`
    - The test script itself is modified
    - The workflow file is modified

    The test will **FAIL** (not skip) if any prerequisites are missing, ensuring 
    issues are caught early in CI/CD pipelines.
"""

import subprocess
import tempfile
import os
import yaml
import unittest
from pathlib import Path
from typing import Dict
import sys

# Configuration (paths relative to repo root)
REPO_ROOT = Path(__file__).parent.parent
CONFIG_FILE = REPO_ROOT / "components/kueue/development/tekton-kueue/config.yaml"
KUSTOMIZATION_FILE = REPO_ROOT / "components/kueue/staging/base/tekton-kueue/kustomization.yaml"


def get_tekton_kueue_image() -> str:
    """Read the tekton-kueue image from the kustomization file."""
    try:
        with open(KUSTOMIZATION_FILE, 'r') as f:
            kustomization = yaml.safe_load(f)
        
        # Look for the tekton-kueue image in the images section
        images = kustomization.get('images', [])
        for image in images:
            if image.get('name') == 'konflux-ci/tekton-kueue':
                new_name = image.get('newName', '')
                new_tag = image.get('newTag', '')
                if new_name and new_tag:
                    return f"{new_name}:{new_tag}"
        
        raise ValueError("tekton-kueue image not found in kustomization")
    
    except Exception as e:
        raise RuntimeError(f"Failed to read tekton-kueue image from {KUSTOMIZATION_FILE}: {e}")

# Test PipelineRun definitions
TEST_PIPELINERUNS = {
    "multiplatform_new": {
        "name": "Multi-platform pipeline (new style with build-platforms parameter)",
        "pipelinerun": {
            "apiVersion": "tekton.dev/v1",
            "kind": "PipelineRun",
            "metadata": {
                "name": "test-multiplatform-new",
                "namespace": "default",
                "labels": {
                    "pipelinesascode.tekton.dev/event-type": "push"
                }
            },
            "spec": {
                "pipelineRef": {"name": "build-pipeline"},
                "params": [
                    {
                        "name": "build-platforms",
                        "value": ["linux/amd64", "linux/arm64", "linux/s390x"]
                    },
                    {"name": "other-param", "value": "test"}
                ],
                "workspaces": [{"name": "shared-workspace", "emptyDir": {}}]
            }
        },
        "expected": {
            "annotations": [
                "kueue.konflux-ci.dev/requests-linux-amd64",
                "kueue.konflux-ci.dev/requests-linux-arm64", 
                "kueue.konflux-ci.dev/requests-linux-s390x"
            ],
            "labels": {
                "kueue.x-k8s.io/queue-name": "pipelines-queue",
                "kueue.x-k8s.io/priority-class": "konflux-post-merge-build"
            }
        }
    },
    
    "multiplatform_old": {
        "name": "Multi-platform pipeline (old style with PLATFORM parameters)",
        "pipelinerun": {
            "apiVersion": "tekton.dev/v1",
            "kind": "PipelineRun",
            "metadata": {
                "name": "test-multiplatform-old",
                "namespace": "default",
                "labels": {
                    "pipelinesascode.tekton.dev/event-type": "pull_request"
                }
            },
            "spec": {
                "pipelineSpec": {
                    "tasks": [
                        {
                            "name": "build-task-amd64",
                            "params": [{"name": "PLATFORM", "value": "linux/amd64"}],
                            "taskRef": {"name": "build-task"}
                        },
                        {
                            "name": "build-task-arm64", 
                            "params": [{"name": "PLATFORM", "value": "linux/arm64"}],
                            "taskRef": {"name": "build-task"}
                        },
                        {
                            "name": "other-task",
                            "taskRef": {"name": "other-task"}
                        }
                    ]
                },
                "workspaces": [{"name": "shared-workspace", "emptyDir": {}}]
            }
        },
        "expected": {
            "annotations": [
                "kueue.konflux-ci.dev/requests-linux-amd64",
                "kueue.konflux-ci.dev/requests-linux-arm64"
            ],
            "labels": {
                "kueue.x-k8s.io/queue-name": "pipelines-queue",
                "kueue.x-k8s.io/priority-class": "konflux-pre-merge-build"
            }
        }
    },
    
    "release_managed": {
        "name": "Release managed pipeline",
        "pipelinerun": {
            "apiVersion": "tekton.dev/v1",
            "kind": "PipelineRun",
            "metadata": {
                "name": "test-release-managed",
                "namespace": "default",
                "labels": {
                    "appstudio.openshift.io/service": "release",
                    "pipelines.appstudio.openshift.io/type": "managed"
                }
            },
            "spec": {
                "pipelineRef": {"name": "release-pipeline"},
                "workspaces": [{"name": "shared-workspace", "emptyDir": {}}]
            }
        },
        "expected": {
            "annotations": [],
            "labels": {
                "kueue.x-k8s.io/queue-name": "pipelines-queue",
                "kueue.x-k8s.io/priority-class": "konflux-release"
            }
        }
    },
    
    "release_tenant": {
        "name": "Release tenant pipeline",
        "pipelinerun": {
            "apiVersion": "tekton.dev/v1",
            "kind": "PipelineRun",
            "metadata": {
                "name": "test-release-tenant",
                "namespace": "default",
                "labels": {
                    "appstudio.openshift.io/service": "release",
                    "pipelines.appstudio.openshift.io/type": "tenant"
                }
            },
            "spec": {
                "pipelineRef": {"name": "release-pipeline"},
                "workspaces": [{"name": "shared-workspace", "emptyDir": {}}]
            }
        },
        "expected": {
            "annotations": [],
            "labels": {
                "kueue.x-k8s.io/queue-name": "pipelines-queue",
                "kueue.x-k8s.io/priority-class": "konflux-tenant-release"
            }
        }
    },
    
    "mintmaker": {
        "name": "Mintmaker dependency update",
        "pipelinerun": {
            "apiVersion": "tekton.dev/v1",
            "kind": "PipelineRun",
            "metadata": {
                "name": "test-mintmaker",
                "namespace": "mintmaker"
            },
            "spec": {
                "pipelineRef": {"name": "dependency-update-pipeline"},
                "workspaces": [{"name": "shared-workspace", "emptyDir": {}}]
            }
        },
        "expected": {
            "annotations": [],
            "labels": {
                "kueue.x-k8s.io/queue-name": "pipelines-queue",
                "kueue.x-k8s.io/priority-class": "konflux-dependency-update"
            }
        }
    },
    
    "integration_test_push": {
        "name": "Integration test pipeline (push event)",
        "pipelinerun": {
            "apiVersion": "tekton.dev/v1",
            "kind": "PipelineRun",
            "metadata": {
                "name": "test-integration-test-push",
                "namespace": "default",
                "labels": {
                    "pac.test.appstudio.openshift.io/event-type": "push"
                }
            },
            "spec": {
                "pipelineRef": {"name": "integration-test-pipeline"},
                "workspaces": [{"name": "shared-workspace", "emptyDir": {}}]
            }
        },
        "expected": {
            "annotations": [],
            "labels": {
                "kueue.x-k8s.io/queue-name": "pipelines-queue",
                "kueue.x-k8s.io/priority-class": "konflux-post-merge-test"
            }
        }
    },
    
    "integration_test_pr": {
        "name": "Integration test pipeline (pull request event)",
        "pipelinerun": {
            "apiVersion": "tekton.dev/v1",
            "kind": "PipelineRun",
            "metadata": {
                "name": "test-integration-test-pr",
                "namespace": "default",
                "labels": {
                    "pac.test.appstudio.openshift.io/event-type": "pull_request"
                }
            },
            "spec": {
                "pipelineRef": {"name": "integration-test-pipeline"},
                "workspaces": [{"name": "shared-workspace", "emptyDir": {}}]
            }
        },
        "expected": {
            "annotations": [],
            "labels": {
                "kueue.x-k8s.io/queue-name": "pipelines-queue",
                "kueue.x-k8s.io/priority-class": "konflux-pre-merge-test"
            }
        }
    },
    
    "default_priority": {
        "name": "Default pipeline (no special labels)",
        "pipelinerun": {
            "apiVersion": "tekton.dev/v1",
            "kind": "PipelineRun",
            "metadata": {
                "name": "test-default",
                "namespace": "default"
            },
            "spec": {
                "pipelineRef": {"name": "default-pipeline"},
                "workspaces": [{"name": "shared-workspace", "emptyDir": {}}]
            }
        },
        "expected": {
            "annotations": [],
            "labels": {
                "kueue.x-k8s.io/queue-name": "pipelines-queue",
                "kueue.x-k8s.io/priority-class": "konflux-default"
            }
        }
    }
}


class TektonKueueMutationTest(unittest.TestCase):
    """Test suite for tekton-kueue CEL expression mutations."""
    
    @classmethod
    def setUpClass(cls):
        """Set up test class - check prerequisites."""
        # Check if config file exists
        if not CONFIG_FILE.exists():
            raise FileNotFoundError(f"Config file not found: {CONFIG_FILE}")
        
        # Check if kustomization file exists
        if not KUSTOMIZATION_FILE.exists():
            raise FileNotFoundError(f"Kustomization file not found: {KUSTOMIZATION_FILE}")
        
        # Get the tekton-kueue image from kustomization
        try:
            cls.tekton_kueue_image = get_tekton_kueue_image()
            print(f"Using tekton-kueue image: {cls.tekton_kueue_image}")
        except Exception as e:
            raise RuntimeError(f"Failed to get tekton-kueue image: {e}")
        
        # Check if podman is available
        try:
            subprocess.run(["podman", "--version"], capture_output=True, check=True)
        except FileNotFoundError:
            raise RuntimeError("podman command not found. Please install podman.")
        except subprocess.CalledProcessError as e:
            raise RuntimeError(f"podman is not working properly: {e}")
    
    def run_mutation_test(self, test_data: Dict) -> Dict:
        """Run a single mutation test and return results."""
        pipelinerun = test_data["pipelinerun"]
        
        with tempfile.TemporaryDirectory() as temp_dir:
            # Write the config file
            config_path = Path(temp_dir) / "config.yaml"
            pipelinerun_path = Path(temp_dir) / "pipelinerun.yaml"
            
            # Copy the config file
            import shutil
            shutil.copy2(CONFIG_FILE, config_path)
            
            # Write the PipelineRun
            with open(pipelinerun_path, 'w') as f:
                yaml.dump(pipelinerun, f, default_flow_style=False)
            
            # Set proper permissions
            os.chmod(config_path, 0o644)
            os.chmod(pipelinerun_path, 0o644)
            os.chmod(temp_dir, 0o755)
            
            # Run the mutation
            cmd = [
                "podman", "run", "--rm",
                "-v", f"{temp_dir}:/workspace:z",
                self.tekton_kueue_image,
                "mutate",
                "--pipelinerun-file", "/workspace/pipelinerun.yaml",
                "--config-dir", "/workspace"
            ]
            
            result = subprocess.run(cmd, capture_output=True, text=True)
            
            if result.returncode != 0:
                self.fail(f"Mutation failed: {result.stderr}")
            
            # Parse the mutated PipelineRun
            try:
                mutated = yaml.safe_load(result.stdout)
            except yaml.YAMLError as e:
                self.fail(f"Failed to parse mutated YAML: {e}")
            
            return mutated
    
    def validate_mutation_result(self, test_key: str, test_data: Dict):
        """Helper method to validate mutation results."""
        with self.subTest(test=test_key):
            mutated = self.run_mutation_test(test_data)
            expected = test_data["expected"]
            
            # Check annotations
            annotations = mutated.get("metadata", {}).get("annotations", {})
            for expected_annotation in expected["annotations"]:
                self.assertIn(expected_annotation, annotations, 
                             f"Expected annotation {expected_annotation} not found")
                self.assertEqual(annotations[expected_annotation], "1",
                               f"Expected annotation {expected_annotation} to have value '1'")
            
            # Check labels
            labels = mutated.get("metadata", {}).get("labels", {})
            for expected_label, expected_value in expected["labels"].items():
                self.assertIn(expected_label, labels,
                             f"Expected label {expected_label} not found")
                self.assertEqual(labels[expected_label], expected_value,
                               f"Expected label {expected_label} to have value '{expected_value}', got '{labels.get(expected_label)}'")
    
    def test_all_mutations(self):
        """Test all tekton-kueue mutation scenarios."""
        for test_key, test_data in TEST_PIPELINERUNS.items():
            self.validate_mutation_result(test_key, test_data)


if __name__ == "__main__":
    import argparse
    
    parser = argparse.ArgumentParser(description="Test tekton-kueue CEL expressions")
    parser.add_argument("--check-setup", action="store_true", 
                       help="Check if prerequisites are met and show configuration")
    parser.add_argument("--verbose", "-v", action="store_true",
                       help="Run tests with verbose output")
    
    # Parse known args to allow unittest args to pass through
    args, unknown = parser.parse_known_args()
    
    if args.check_setup:
        print("Checking prerequisites...")
        
        # Check config file
        if CONFIG_FILE.exists():
            print(f"✓ Config file found: {CONFIG_FILE}")
        else:
            print(f"✗ Config file not found: {CONFIG_FILE}")
            sys.exit(1)
        
        # Check kustomization file
        if KUSTOMIZATION_FILE.exists():
            print(f"✓ Kustomization file found: {KUSTOMIZATION_FILE}")
        else:
            print(f"✗ Kustomization file not found: {KUSTOMIZATION_FILE}")
            sys.exit(1)
        
        # Check tekton-kueue image
        try:
            image = get_tekton_kueue_image()
            print(f"✓ Tekton-kueue image: {image}")
        except Exception as e:
            print(f"✗ Failed to get tekton-kueue image: {e}")
            sys.exit(1)
        
        # Check podman
        try:
            result = subprocess.run(["podman", "--version"], capture_output=True, check=True, text=True)
            print(f"✓ Podman available: {result.stdout.strip()}")
        except (subprocess.CalledProcessError, FileNotFoundError):
            print("✗ Podman is not available")
            sys.exit(1)
        
        print("\n✅ All prerequisites met! Ready to run tests.")
        print("Run: python hack/test-tekton-kueue-config.py")
        print("\nNote: Tests will FAIL (not skip) if any prerequisites are missing.")
        
    else:
        # Run unittest with remaining args
        verbosity = 2 if args.verbose else 1
        sys.argv = [sys.argv[0]] + unknown
        unittest.main(verbosity=verbosity) 
