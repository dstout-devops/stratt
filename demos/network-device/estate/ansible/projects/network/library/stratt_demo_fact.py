#!/usr/bin/python
# A module this REPO ships — the `library/` case ANS-006 projects and the parity register could
# not say whether ansible could actually load (docs/parity/ansible-tool.md, `--module-path`).
#
# Projecting that a module exists and ansible LOADING it are different claims, and the row
# conflated them. This file exists so a Run answers the second one: it lives in the project's own
# library/, nothing declares a module path, and a play calls it by name. If ansible finds it, the
# automatic `library/`-beside-the-playbook search covers the case and no knob is needed.
from ansible.module_utils.basic import AnsibleModule


def main() -> None:
    module = AnsibleModule(argument_spec={"marker": {"type": "str", "required": True}})
    module.exit_json(changed=False, seen=module.params["marker"])


if __name__ == "__main__":
    main()
