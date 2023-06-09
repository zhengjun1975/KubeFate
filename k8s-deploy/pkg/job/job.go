/*
 * Copyright 2019-2022 VMware, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

// job
package job

import (
	"fmt"
	"time"

	"github.com/FederatedAI/KubeFATE/k8s-deploy/pkg/modules"
	"github.com/FederatedAI/KubeFATE/k8s-deploy/pkg/service"
	"github.com/rs/zerolog/log"
)

const (
	fateChartName = "fate"
)

func stopJob(job *modules.Job, cluster *modules.Cluster) bool {
	if !cluster.IsExisted(cluster.Name, cluster.NameSpace) {
		return true
	}

	if !job.IsExisted(job.Uuid) {
		return true
	}

	return false
}

func getClusterComponentsStatus(clusterName, clusterNamespace string) (map[string]string, error) {
	deploymentStatus, err := service.GetClusterDeployStatus(clusterName, clusterNamespace)
	if err != nil {
		log.Error().Err(err).Msg("GetClusterDeployStatus error")
		return deploymentStatus, err
	}
	stsStatus, err := service.GetClusterStsStatus(clusterName, clusterNamespace)
	if err != nil {
		log.Error().Err(err).Msg("GetClusterStsStatus error")
		return deploymentStatus, err
	}
	for k, v := range stsStatus {
		deploymentStatus[k] = v
	}
	return deploymentStatus, nil
}

func generateSubJobs(job *modules.Job, clusterComponentStatus map[string]string) modules.SubJobs {

	subJobs := make(modules.SubJobs)
	if job.SubJobs != nil {
		subJobs = job.SubJobs
	}

	// The cluster component status includes deployments and statefulSets
	for k, v := range clusterComponentStatus {
		var subJobStatus string = "Running"
		if service.CheckStatus(v) {
			subJobStatus = "Success"
		}

		var subJob modules.SubJob
		if _, ok := subJobs[k]; !ok {
			subJob = modules.SubJob{
				ModuleName:    k,
				Status:        subJobStatus,
				ModulesStatus: v,
				StartTime:     job.StartTime,
			}
		} else {
			subJob = subJobs[k]
			subJob.Status = subJobStatus
			subJob.ModulesStatus = v
		}

		if subJobStatus == "Success" && subJob.EndTime.IsZero() {
			subJob.EndTime = time.Now()
		}

		subJobs[k] = subJob
		log.Debug().Interface("subJob", subJob).Msg("generate SubJobs")
	}

	job.SubJobs = subJobs
	return subJobs
}

func ClusterUpdate(clusterArgs *modules.ClusterArgs, creator string) (*modules.Job, error) {
	// Check whether the cluster exists
	c := new(modules.Cluster)
	if ok := c.IsExisted(clusterArgs.Name, clusterArgs.Namespace); !ok {
		return nil, fmt.Errorf("name=%s Cluster is not existed", clusterArgs.Name)
	}

	c = &modules.Cluster{Name: clusterArgs.Name, NameSpace: clusterArgs.Namespace}
	cluster, err := c.Get()
	if err != nil {
		log.Error().Err(err).Interface("clusterArgs", clusterArgs).Msg("Find Cluster by clusterArgs error")
		return nil, err
	}

	clusterNew, err := modules.NewCluster(clusterArgs.Name, clusterArgs.Namespace, clusterArgs.ChartName, clusterArgs.ChartVersion, string(clusterArgs.Data))
	if err != nil {
		log.Error().Err(err).Msg("NewCluster")
		return nil, err
	}

	var specOld = cluster.Spec
	var specNew = clusterNew.Spec
	var valuesOld = cluster.Values
	var valuesNew = clusterNew.Values

	var um UpgradeManager
	switch cluster.ChartName {
	case fateChartName:
		um = &FateUpgradeManager{
			namespace: clusterArgs.Namespace,
		}
	default:
		um = &FallbackUpgradeManager{}
		log.Info().Msgf("no upgrade manager is available for %s", cluster.Name)
	}
	err = um.validate(specOld, specNew)
	if err != nil {
		return nil, err
	}

	job := modules.NewJob(clusterArgs, "ClusterUpdate", creator, cluster.Uuid)
	//  save job to modules
	_, err = job.Insert()
	if err != nil {
		log.Error().Err(err).Interface("job", job).Msg("save job error")
		return nil, err
	}

	log.Info().Str("jobId", job.Uuid).Msg("create a new job of ClusterUpdate")

	//do job
	go func() {
		// update job.status/ cluster.status / cluster
		dbErr := job.SetStatus(modules.JobStatusRunning)
		if dbErr != nil {
			log.Error().Err(dbErr).Msg("job.SetStatus error")
		}

		dbErr = cluster.SetStatus(modules.ClusterStatusUpdating)
		if dbErr != nil {
			log.Error().Err(dbErr).Msg("Cluster.SetStatus error")
		}
		umCluster := um.getCluster(specOld, specNew)
		if umCluster.Name != "fallbackUM" && specOld["chartVersion"].(string) != specNew["chartVersion"].(string) {
			// We will implicitly install a new cluster for the upgrade manager, and delete it after it finishes its job
			err := umCluster.HelmInstall()
			if err != nil {
				log.Error().Err(err).Msgf("failed to install the upgrade manager's helm chart for cluster %s", cluster.ChartName)
				dbErr := job.SetStatus(modules.JobStatusFailed)
				if dbErr != nil {
					log.Error().Err(dbErr).Msg("job.SetStatus error")
				}
				job.Status = modules.JobStatusFailed
				log.Error().Msg("abort upgrade because failed to install upgrade manager")
				return
			}
			finished := um.waitFinish(30, 20)
			if !finished {
				dbErr := job.SetStatus(modules.JobStatusFailed)
				if dbErr != nil {
					log.Error().Err(dbErr).Msg("job.SetStatus error")
				}
			}
			if !clusterArgs.KeepUpgradeJob {
				err = umCluster.HelmDelete()
				if err != nil {
					log.Error().Err(err).Msg("failed to delete the upgrade manager cluster, need a person to investigate why")
				}
			}
			if job.Status == modules.JobStatusFailed {
				log.Error().Msg("abort upgrade because upgrade manager cannot finish its job")
				return
			}
		}

		dbErr = cluster.SetValues(valuesNew)
		if dbErr != nil {
			log.Error().Err(dbErr).Msg("Cluster.SetSpec error")
		}
		dbErr = cluster.SetSpec(specNew)
		if dbErr != nil {
			log.Error().Err(dbErr).Msg("Cluster.SetSpec error")
		}

		// HelmUpgrade

		//The Chart version does not change and update is used
		//Upgrade corresponding to Helm
		cluster.ChartName = clusterArgs.ChartName
		cluster.ChartVersion = clusterArgs.ChartVersion
		err = cluster.HelmUpgrade()
		cluster.HelmRevision += 1

		_, dbErr = cluster.UpdateByUuid(job.ClusterId)
		if dbErr != nil {
			log.Error().Err(dbErr).Interface("cluster", cluster).Msg("Update Cluster error")
		}

		if err != nil {
			log.Error().Err(err).Str("ClusterId", cluster.Uuid).Msg("Helm upgrade Cluster error")

			dbErr := job.SetState(err.Error())
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("job.SetResult error")
			}
			dbErr = job.SetStatus(modules.JobStatusFailed)
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("job.SetStatus error")
			}
		} else {
			log.Debug().Str("ClusterId", cluster.Uuid).Msg("helm upgrade Cluster Success")

			dbErr := job.SetState("Cluster update Success")
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("job.SetResult error")
			}
			dbErr = job.SetStatus(modules.JobStatusRunning)
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("job.SetStatus error")
			}
		}

		for job.Status == modules.JobStatusRunning {
			if stopJob(job, &cluster) {
				continue
			}

			if job.TimeOut() {
				dbErr := job.SetState("Checkout Cluster status timeOut!")
				if dbErr != nil {
					log.Error().Err(dbErr).Msg("job.SetResult error")
				}
				dbErr = job.SetStatus(modules.JobStatusFailed)
				if dbErr != nil {
					log.Error().Err(dbErr).Msg("job.SetStatus error")
				}
				break
			}

			// update subJobs
			clusterComponentStatus, err := getClusterComponentsStatus(clusterArgs.Name, clusterArgs.Namespace)
			if err != nil {
				log.Error().Err(err).Msg("GetClusterDeployStatus error")
			}

			subJobs := generateSubJobs(job, clusterComponentStatus)

			dbErr = job.SetSubJobs(subJobs)
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("job.SetSubJobs error")
			}

			if service.CheckClusterStatus(clusterComponentStatus) {
				dbErr := job.SetStatus(modules.JobStatusSuccess)
				if dbErr != nil {
					log.Error().Err(dbErr).Msg("job.SetStatus error")
				}
				break
			}
			time.Sleep(5 * time.Second)
		}

		if job.Status == modules.JobStatusCanceled {
			dbErr := job.SetState("Job canceled")
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("job.SetResult error")
			}
		}

		// save cluster to modules
		if job.Status == modules.JobStatusSuccess {
			cluster.Status = modules.ClusterStatusRunning
			cluster.Revision++
			_, err = cluster.UpdateByUuid(job.ClusterId)
			if err != nil {
				log.Error().Err(err).Interface("cluster", cluster).Msg("Update Cluster error")
			}
			log.Debug().Str("cluster uuid", cluster.Uuid).Msg("Update Cluster Success")
		}

		// rollBACK
		if job.Status != modules.JobStatusSuccess && job.Status != modules.JobStatusCanceled {
			dbErr = cluster.SetValues(valuesOld)
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("Cluster.SetSpec error")
			}
			dbErr = cluster.SetSpec(specOld)
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("Cluster.SetSpec error")
			}
			dbErr = cluster.SetStatus(modules.ClusterStatusRollback)
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("Cluster.SetStatus error")
			}

			//The Chart version does not change and update is used
			//Upgrade corresponding to Helm
			err = cluster.HelmRollback()
			cluster.HelmRevision -= 1

			if err != nil {
				log.Error().Err(err).Str("ClusterId", cluster.Uuid).Msg("Helm upgrade Cluster error")

				dbErr := job.SetState(err.Error())
				if dbErr != nil {
					log.Error().Err(dbErr).Msg("job.SetResult error")
				}
				dbErr = job.SetStatus(modules.JobStatusFailed)
				if dbErr != nil {
					log.Error().Err(dbErr).Msg("job.SetStatus error")
				}
			} else {
				log.Debug().Str("ClusterId", cluster.Uuid).Msg("Helm upgrade Cluster Success")

				dbErr := job.SetState("Cluster run rollback Success")
				if dbErr != nil {
					log.Error().Err(dbErr).Msg("job.SetResult error")
				}
				dbErr = job.SetStatus(modules.JobStatusRollback)
				if dbErr != nil {
					log.Error().Err(dbErr).Msg("job.SetStatus error")
				}
			}

			//
			for job.Status == modules.JobStatusRunning {
				if job.TimeOut() {
					dbErr := job.SetState("Checkout Cluster status timeOut!")
					if dbErr != nil {
						log.Error().Err(dbErr).Msg("job.SetResult error")
					}
					dbErr = job.SetStatus(modules.JobStatusFailed)
					if dbErr != nil {
						log.Error().Err(dbErr).Msg("job.SetStatus error")
					}
					break
				}

				// update subJobs
				clusterComponentStatus, err := getClusterComponentsStatus(clusterArgs.Name, clusterArgs.Namespace)
				if err != nil {
					log.Error().Err(err).Msg("clusterComponentStatus error")
				}

				log.Debug().Interface("clusterComponentStatus", clusterComponentStatus).Msg("clusterComponentStatus()")

				subJobs := generateSubJobs(job, clusterComponentStatus)

				dbErr = job.SetSubJobs(subJobs)
				if dbErr != nil {
					log.Error().Err(dbErr).Msg("job.SetSubJobs error")
				}

				if service.CheckClusterStatus(clusterComponentStatus) {
					dbErr := job.SetStatus(modules.JobStatusSuccess)
					if dbErr != nil {
						log.Error().Err(dbErr).Msg("job.SetStatus error")
					}
					break
				}
				time.Sleep(5 * time.Second)
			}

			_, err = cluster.UpdateByUuid(cluster.Uuid)
			if err != nil {
				log.Error().Err(err).Interface("cluster", cluster).Msg("RollBACK Cluster error")
			}
			log.Debug().Str("cluster uuid", cluster.Uuid).Msg("RollBACK Cluster Success")
		}

		job.EndTime = time.Now()
		_, err = job.UpdateByUuid(job.Uuid)
		if err != nil {
			log.Error().Err(err).Str("jobId", job.Uuid).Msg("update job By Uuid error")
		}

		if job.Status == modules.JobStatusSuccess {
			log.Debug().Interface("job", job).Msg("job run Success")
		} else {
			log.Warn().Interface("job", job).Msg("job run failed")
		}
	}()

	return job, nil
}

func ClusterDelete(clusterId string, creator string) (*modules.Job, error) {
	if clusterId == "" {
		return nil, fmt.Errorf("clusterID cannot be empty")
	}

	c := modules.Cluster{Uuid: clusterId}
	cluster, err := c.Get()
	if err != nil {
		log.Error().Err(err).Interface("clusterID", clusterId).Msg("Find Cluster by clusterId error")
		return nil, err
	}

	job := modules.NewJob(nil, "ClusterDelete", creator, clusterId)
	// save job to modules
	_, err = job.Insert()
	if err != nil {
		log.Err(err).Interface("job", job).Msg("Save job error")
		return nil, err
	}

	log.Info().Str("jobId", job.Uuid).Msg("Create a new job of ClusterDelete")

	go func() {
		dbErr := job.SetStatus(modules.JobStatusRunning)
		if dbErr != nil {
			log.Error().Err(dbErr).Msg("job.SetStatus error")
		}
		dbErr = cluster.SetStatus(modules.ClusterStatusDeleting)
		if dbErr != nil {
			log.Error().Err(dbErr).Msg("Cluster.SetStatus error")
		}

		err = cluster.HelmDelete()
		if err != nil {
			dbErr := job.SetState(err.Error())
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("job.SetResult error")
			}
			job.Status = modules.JobStatusFailed
			log.Err(err).Str("ClusterId", cluster.Uuid).Msg("Helm delete Cluster error")
		} else {
			dbErr := job.SetState("uninstall Success")
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("job.SetResult error")
			}
			job.Status = modules.JobStatusRunning
			log.Debug().Str("ClusterId", cluster.Uuid).Msg("Helm delete Cluster Success")
		}

		if job.Status == modules.JobStatusRunning {
			job.Status = modules.JobStatusSuccess
		}

		if job.Status == modules.JobStatusCanceled {
			dbErr := job.SetState("Job canceled")
			if dbErr != nil {
				log.Error().Err(dbErr).Msg("job.SetResult error")
			}
		}

		//if job.Status == modules.JobStatusSuccess {
		c := modules.Cluster{Uuid: clusterId}
		_, err = c.Delete()
		if err != nil {
			log.Err(err).Interface("cluster", cluster).Msg("modules delete Cluster error")
		}
		log.Debug().Str("clusterUuid", clusterId).Msg("modules delete Cluster Success")

		job.EndTime = time.Now()
		_, err = job.UpdateByUuid(job.Uuid)
		if err != nil {
			log.Err(err).Str("jobId", job.Uuid).Msg("update job By Uuid error")
		}
		if job.Status == modules.JobStatusSuccess {
			log.Debug().Interface("job", job).Msg("job run Success")
		} else {
			log.Warn().Interface("job", job).Msg("job run failed")
		}
	}()

	return job, nil
}
