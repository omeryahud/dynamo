# 2. Install CRDs (skip if on shared cluster where CRDs already exist)
rm -rf ./dynamo-crds
helm fetch https://helm.ngc.nvidia.com/nvidia/ai-dynamo/charts/dynamo-crds-0.7.1.tgz
tar -xvzf dynamo-crds-0.7.1.tgz
helm upgrade -i --timeout 10m dynamo-crds ./dynamo-crds --namespace default
rm dynamo-crds-0.7.1.tgz


# 3. Install Platform
rm -rf ./dynamo-platform
helm fetch https://helm.ngc.nvidia.com/nvidia/ai-dynamo/charts/dynamo-platform-0.7.1.tgz
tar -xvzf dynamo-platform-0.7.1.tgz
helm upgrade -i --timeout 10m dynamo-platform ./dynamo-platform --set grove.enabled=true --namespace dynamo-system --create-namespace
rm dynamo-platform-0.7.1.tgz