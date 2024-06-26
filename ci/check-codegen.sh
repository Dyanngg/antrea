#!/usr/bin/env bash

# Copyright 2019 Antrea Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# This script makes sure that the checked-in generated code is up-to-date.

set -eo pipefail

function echoerr {
    >&2 echo "$@"
}

THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
pushd $THIS_DIR/.. > /dev/null

make codegen
cd multicluster;make codegen;cd ..
diff="$(git status --porcelain pkg multicluster)"

if [ ! -z "$diff" ]; then
    echoerr "The generated code is not up-to-date"
    echo "The different files are:"
    echo "$diff"
    echoerr "You can regenerate it with 'make codegen' or 'cd multicluster;make codegen', and commit the changes"
    exit 1
fi
