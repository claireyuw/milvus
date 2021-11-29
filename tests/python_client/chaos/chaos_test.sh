set -e
set -x


echo "check os env"
platform='unknown'
unamestr=$(uname)
if [[ "$unamestr" == 'Linux' ]]; then
   platform='Linux'
elif [[ "$unamestr" == 'Darwin' ]]; then
   platform='Mac'
fi
echo "platform: $platform"

ns="chaos-testing"

# switch namespace
kubectl config set-context --current --namespace=${ns}

# set parameters
pod=${1:-"querynode"}
chaos_type=${2:-"pod_kill"} #pod_kill or pod_failure
chaos_task=${3:-"chaos-test"} # chaos-test or data-consist-test 

release="test"-${pod}-${chaos_type/_/-} # replace pod_kill to pod-kill

# install milvus cluster for chaos testing
pushd ./scripts
echo "uninstall milvus if exist"
bash uninstall_milvus.sh ${release} ${ns}|| true
echo "install milvus"
if [ ${pod} != "standalone" ];
then
    echo "insatll cluster"
    bash install_milvus.sh ${release} ${ns}
fi

if [ ${pod} == "standalone" ];
then
    echo "install standalone"
    helm install --wait --timeout 360s ${release} milvus/milvus --set service.type=NodePort -f ../standalone-values.yaml -n=${ns}
fi
# if chaos_type is pod_failure, update replicas
if [ "$chaos_type" == "pod_failure" ];
then
    declare -A pod_map=(["querynode"]="queryNode" ["indexnode"]="indexNode" ["datanode"]="dataNode" ["proxy"]="proxy")
    helm upgrade ${release} milvus/milvus --set ${pod_map[${pod}]}.replicas=2 --reuse-values
fi

# wait all pod ready
kubectl wait --for=condition=Ready pod -l app.kubernetes.io/instance=milvus-chaos -n chaos-testing --timeout=360s
kubectl wait --for=condition=Ready pod -l release=milvus-chaos -n chaos-testing --timeout=360s

popd

# replace chaos object as defined
if [ "$platform" == "Mac" ];
then
    sed -i "" "s/TESTS_CONFIG_LOCATION =.*/TESTS_CONFIG_LOCATION = \'chaos_objects\/${chaos_type}\/'/g" constants.py
    sed -i "" "s/ALL_CHAOS_YAMLS =.*/ALL_CHAOS_YAMLS = \'chaos_${pod}_${chaos_type}.yaml\'/g" constants.py
else
    sed -i "s/TESTS_CONFIG_LOCATION =.*/TESTS_CONFIG_LOCATION = \'chaos_objects\/${chaos_type}\/'/g" constants.py
    sed -i "s/ALL_CHAOS_YAMLS =.*/ALL_CHAOS_YAMLS = \'chaos_${pod}_${chaos_type}.yaml\'/g" constants.py
fi

# run chaos testing
echo "start running testcase ${pod}"
if [[ $release =~ "milvus" ]]
then
    host=$(kubectl get svc/${release} -o jsonpath="{.spec.clusterIP}")
else
    host=$(kubectl get svc/${release}-milvus -o jsonpath="{.spec.clusterIP}")
fi

python scripts/hello_milvus.py --host "$host"
# chaos test
export ENABLE_TRACEBACK=False

if [ "$chaos_task" == "chaos-test" ];
then
    pytest -s -v test_chaos.py --host "$host" --log-cli-level=INFO --capture=no || echo "chaos test fail"
fi
# data consist test
if [ "$chaos_task" == "data-consist-test" ];
then
    pytest -s -v test_chaos_data_consist.py --host "$host" --log-cli-level=INFO --capture=no || echo "chaos test fail"
fi
sleep 30
echo "start running e2e test"
kubectl wait --for=condition=Ready pod -l app.kubernetes.io/instance=${release} -n chaos-testing --timeout=360s
kubectl wait --for=condition=Ready pod -l release=${release} -n chaos-testing --timeout=360s

python scripts/hello_milvus.py --host "$host" || echo "e2e test fail"

# save logs
data=`date +%Y-%m-%d-%H-%M-%S`
bash ../../scripts/export_log_k8s.sh ${ns} ${release} k8s_log/${pod}-${chaos_type}-${chaos_task}-${data}
