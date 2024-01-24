# Copyright (C) 2024 VMware, Inc. All rights reserved.
# -- VMware Confidential

class AntreaNPProvider(DebugProvider):

    def run(self):
        agentName = self.ctx.get('agent_name')
        cache = exec_command_in_pod(
            name=agentName,
            namespace='kube-system',
            container='antrea-agent',
            command=[
                "/bin/bash",
                "-c",
                "antctl version",
            ])
        logger.info('============= is {}'.format(cache))
        self.ctx['cache'] = cache

    def next(self):
        return TerminalDecision(
            self,
            result=ResultMessage(
                self.runbook_id,
                "ANTREA",
                [self.ctx['cache']],
            ),
            rcmd=None
        )

    @property
    def result_description(self):
        return StepMessage(self.runbook_id, "HAHA")

