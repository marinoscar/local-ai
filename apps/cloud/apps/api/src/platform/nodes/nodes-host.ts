// The two host ports the platform's nodes slice (`NodesModule`) needs, bound to inert answers.
//
// The nodes slice moves a held job's bytes through short-lived signed object-storage URLs (the "data plane"). Local AI
// uses the worker-node plane only for registration, heartbeat and health (docs/specs/cloud-app.md section 3.6): no
// EAB job is node-eligible in v1 (section 6) and this app enables no object-storage slice, so there is nothing to
// sign. Rather than leave the ports unbound (the app would not boot) or invent a storage backend, both refuse
// loudly if a data-plane route is ever reached. Replace them when an object store and node-eligible jobs exist.
import { Injectable, ServiceUnavailableException } from '@nestjs/common';
import { NodeJobInputError, type NodeJobInputObject, type NodeJobInputs, type NodeObjectStore } from '@marinoscar/platform-api/nodes';
import type { Job } from '@marinoscar/platform-api/jobs';

/** `NODE_OBJECT_STORE`: no object storage is configured for node data transfer. */
@Injectable()
export class NoNodeObjectStore implements NodeObjectStore {
  async getSignedDownloadUrl(): Promise<string> {
    throw new ServiceUnavailableException('No object storage is configured for node data transfer');
  }

  async getSignedPutUrl(): Promise<string> {
    throw new ServiceUnavailableException('No object storage is configured for node data transfer');
  }
}

/** `NODE_JOB_INPUTS`: no job type of this app has a downloadable input object. */
@Injectable()
export class NoNodeJobInputs implements NodeJobInputs {
  async resolve(job: Job): Promise<NodeJobInputObject> {
    throw new NodeJobInputError('input_object_not_found', 'This app has no node job inputs', job.id, job.subjectId ?? null);
  }
}
