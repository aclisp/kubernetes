# Admission controller plug-in for YY RDS/Sigma

Under this directory is a set of plug-in I wrote for YY RDS/Sigma project. 
Contact huanghao@yy.com for more details. 

## What is Kubernetes?

> Kubernetes is an open source orchestration system for Docker containers.

> Using the concepts of "labels" and "pods", it groups the containers which make up
> an application into logical units for easy management and discovery.

It is called *k8s* for short.

## What is RDS/Sigma project?

The RDS/Sigma project make use of k8s. 

Its purpose is to dockerize application instances so that they are *forced* to be 
isolated in the container boundary in regards of resource usage such as CPU, memory 
and disk space. 

We also want to make these resources, which are summed from many bear metal hosts, 
a pool, with a scheduler to do resource allocation and placement in a global view.

*Sigma* is the symbol (σ) indicating the addition of the numbers or quantities. 
We name our project as it. We need somehow an open source orchestration system for 
Docker containers, and so we chose k8s. 

## Why the plug-ins?

The reason is to build customized admission controllers for YY application instances.

As you may know,

> An admission control plug-in is a piece of code that intercepts requests to the 
> Kubernetes API server prior to persistence of the object, but after the request 
> is authenticated and authorized.

> If any of the plug-ins in the sequence reject the request, the entire request is 
> rejected immediately and an error is returned to the end-user.

> Admission control plug-ins may mutate the incoming object in some cases to apply 
> system configured defaults. 

There are some ideas come in to my head,

*   K8s allow pods with no limits (unset values). But we need to enforce every pod has 
    a default CPU/mem/disk limits if the user forget (or intentionally not) to set them.
    
    This could be a sanity checker preventing the node resource from over committing. 
        
*   K8s schedule unassigned (fresh, pending) pods and place them to the node that has
    enough free available resources. However if the pod is already assigned to a node 
    upon its creation, the scheduler will not kick in. 
     
    This is also a sanity checker preventing the node resource from over committing.

*   K8s only supported CPU and memory limits at this time. We have a plan to develop 
    a disk space limits. It is not only in the scope of admission controller, but also
    include extensions in node capacity and persistent volume.
    
BTW, why k8s does not support disk space limit? 

Because generally persistent storage in a CaaS (Containers-as-a-Service) cloud uses 
network attached volumes like Amazon Web Services (AWS) [EBS Volume](http://aws.amazon.com/ebs/).

To achieve high disk I/O performace, especially for RDS MySQL instances, we use host 
SSD as the persistent storage for containers.  
