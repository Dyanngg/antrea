# Copyright (C) 2024 VMware, Inc. All rights reserved.
# -- VMware Confidential

from yaml import safe_load

from sha.contrib.runbook.utils_northstar.shared import get_pod_status_via_api
from sha.contrib.runbook.utils_northstar.pns import get_pns_state, get_pns_framework_status, \
    collect_pns_status, dump_pns_bundle
from sha.contrib.runbook.utils_northstar.k8s_client import get_node_of_pending_pod, get_nsx_node_agent, \
    exec_command_in_pod, get_antrea_controller_pod, K8sClient

expected_connectivity_mapping = {
    'Allow': 'Delivered',
    'Drop': 'Dropped',
    'Reject': 'Rejected',
    'Isolate': 'Dropped',
    '<NONE>': 'Delivered',
}

__all__ = [
    'safe_load',
    'get_pod_status_via_api',
    'get_pns_state',
    'get_pns_framework_status',
    'collect_pns_status',
    'dump_pns_bundle',
    'get_node_of_pending_pod',
    'get_nsx_node_agent',
    'exec_command_in_pod',
    'get_antrea_controller_pod',
    'K8sClient',
    'expected_connectivity_mapping'
]
