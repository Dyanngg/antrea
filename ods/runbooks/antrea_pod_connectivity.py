# Copyright (C) 2024 VMware, Inc. All rights reserved.
# -- VMware Confidential


class AntreaNPEvaluationProvider(DebugProvider):

    def run(self):
        antrea_controller_pod = get_antrea_controller_pod()
        if antrea_controller_pod is None:
            logger.info('failed to retrieve Antrea controller Pod in the cluster')
            return
        self.ctx['antrea_controller_pod'] = antrea_controller_pod.metadata.name
        antctl_np_eval_cmd = 'antctl query networkpolicyevaluation -S {} -D {}'.format(self.ctx['src-pod'], self.ctx['dst-pod'])
        np_eval_result = exec_command_in_pod(
            name=self.ctx['antrea_controller_pod'],
            namespace='kube-system',
            container='antrea-controller',
            command=[
                "/bin/bash",
                "-c",
                antctl_np_eval_cmd,
            ])
        # TODO: think of a way to use structural API
        np_eval_verdict = [result for result in np_eval_result.split(' ') if result.strip()][-1].strip()
        self.ctx['np_eval_verdict'] = np_eval_verdict

    def next(self):
        if self.ctx.get('antrea_controller_pod') is None:
            return TerminalDecision(
                self,
                result=ResultMessage(
                    self.runbook_id,
                    "failed_to_retrieve_antrea_controller_pod",
                ),
                rcmd=RcmdMessage(self.runbook_id, "check_antrea_deployment")
            )
        if self.ctx.get('np_eval_verdict') not in expected_connectivity_mapping:
            return TerminalDecision(
                self,
                result=ResultMessage(
                    self.runbook_id,
                    "failed_to_parse_network_policy_evaluation",
                    [self.ctx.get('np_eval_verdict')]
                ),
                rcmd=RcmdMessage(self.runbook_id, "check_antrea_controller_logs")
            )
        return JumpToDecision(self, 'CheckTraceflowResult')

    @property
    def result_description(self):
        if self.ctx.get('np_eval_verdict') in expected_connectivity_mapping:
            return StepMessage(
                self.runbook_id,
                "network_policy_evaluation_success",
                [self.ctx.get('np_eval_verdict')]
            )
        else:
            return StepMessage(
                self.runbook_id,
                "network_policy_evaluation_failed",
                [self.ctx.get('np_eval_verdict')]
            )


class AntreaTraceflowProvider(DebugProvider):

    def run(self):
        antctl_traceflow_cmd = 'antctl tf -S {} -D {}'.format(self.ctx['src-pod'], self.ctx['dst-pod'])
        tf_result = exec_command_in_pod(
            name=self.ctx['antrea_controller_pod'],
            namespace='kube-system',
            container='antrea-controller',
            command=[
                "/bin/bash",
                "-c",
                antctl_traceflow_cmd,
            ])
        tf_verdict = ''
        tf_res_yml = safe_load(tf_result)
        if 'Error' in tf_res_yml:
            self.ctx['tf_error'] = tf_res_yml['Error']
            return
        if 'results' in tf_res_yml and len(tf_res_yml['results']) > 0:
            tf_final_of_event = tf_res_yml['results'][-1]
            if 'observations' in tf_final_of_event and len(tf_final_of_event['observations']) > 0:
                tf_verdict = tf_final_of_event['observations'][-1]['action']
        self.ctx['tf_verdict'] = tf_verdict
        np_eval_result = self.ctx.get('np_eval_verdict')
        if expected_connectivity_mapping.get(np_eval_result) == tf_verdict:
            self.ctx['connectivity_check_successful'] = True
        else:
            self.ctx['connectivity_check_successful'] = False

    def next(self):
        tf_result = self.ctx.get('tf_verdict')
        if self.ctx.get('tf_error'):
            tf_result = self.ctx.get('tf_error')
        if not tf_result or tf_result not in expected_connectivity_mapping.values():
            return TerminalDecision(
                self,
                result=ResultMessage(
                    self.runbook_id,
                    "failed_to_parse_traceflow",
                    [tf_result],
                ),
                rcmd=RcmdMessage(self.runbook_id, "check_antrea_controller_logs")
            )
        if self.ctx.get('connectivity_check_successful') is True:
            return TerminalDecision(
                self,
                result=ResultMessage(
                    self.runbook_id,
                    "traffic_match_expectation"
                ),
                rcmd=None
            )
        else:
            return TerminalDecision(
                self,
                result=ResultMessage(
                    self.runbook_id,
                    "traffic_does_not_match_expectation",
                    [self.ctx.get('np_eval_verdict'), tf_result]
                ),
                rcmd=RcmdMessage(self.runbook_id, "check_ovs_datapath_rules")
            )

    @property
    def result_description(self):
        tf_result = self.ctx.get('tf_verdict')
        if self.ctx.get('tf_error'):
            tf_result = self.ctx.get('tf_error')
        if not tf_result or tf_result not in expected_connectivity_mapping.values():
            return StepMessage(self.runbook_id, "traceflow_failed", [tf_result])
        else:
            return StepMessage(self.runbook_id, "traceflow_success", [tf_result])
