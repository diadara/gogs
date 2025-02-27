// Copyright 2014 The Gogs Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package models

import (
	"fmt"

	"github.com/gogits/gogs/modules/log"
)

type AccessMode int

const (
	ACCESS_MODE_NONE AccessMode = iota
	ACCESS_MODE_READ
	ACCESS_MODE_WRITE
	ACCESS_MODE_ADMIN
	ACCESS_MODE_OWNER
)

// Access represents the highest access level of a user to the repository. The only access type
// that is not in this table is the real owner of a repository. In case of an organization
// repository, the members of the owners team are in this table.
type Access struct {
	ID     int64 `xorm:"pk autoincr"`
	UserID int64 `xorm:"UNIQUE(s)"`
	RepoID int64 `xorm:"UNIQUE(s)"`
	Mode   AccessMode
}

func accessLevel(e Engine, u *User, repo *Repository) (AccessMode, error) {
	mode := ACCESS_MODE_NONE
	if !repo.IsPrivate {
		mode = ACCESS_MODE_READ
	}

	if u != nil {
		if u.Id == repo.OwnerID {
			return ACCESS_MODE_OWNER, nil
		}

		a := &Access{UserID: u.Id, RepoID: repo.ID}
		if has, err := e.Get(a); !has || err != nil {
			return mode, err
		}
		return a.Mode, nil
	}

	return mode, nil
}

// AccessLevel returns the Access a user has to a repository. Will return NoneAccess if the
// user does not have access. User can be nil!
func AccessLevel(u *User, repo *Repository) (AccessMode, error) {
	return accessLevel(x, u, repo)
}

func hasAccess(e Engine, u *User, repo *Repository, testMode AccessMode) (bool, error) {
	mode, err := accessLevel(e, u, repo)
	return testMode <= mode, err
}

// HasAccess returns true if someone has the request access level. User can be nil!
func HasAccess(u *User, repo *Repository, testMode AccessMode) (bool, error) {
	return hasAccess(x, u, repo, testMode)
}

// GetAccessibleRepositories finds all repositories where a user has access to,
// besides he/she owns.
func (u *User) GetAccessibleRepositories() (map[*Repository]AccessMode, error) {
	accesses := make([]*Access, 0, 10)
	if err := x.Find(&accesses, &Access{UserID: u.Id}); err != nil {
		return nil, err
	}

	repos := make(map[*Repository]AccessMode, len(accesses))
	for _, access := range accesses {
		repo, err := GetRepositoryByID(access.RepoID)
		if err != nil {
			if IsErrRepoNotExist(err) {
				log.Error(4, "%v", err)
				continue
			}
			return nil, err
		}
		if err = repo.GetOwner(); err != nil {
			return nil, err
		} else if repo.OwnerID == u.Id {
			continue
		}
		repos[repo] = access.Mode
	}

	// FIXME: should we generate an ordered list here? Random looks weird.
	return repos, nil
}

func maxAccessMode(modes ...AccessMode) AccessMode {
	max := ACCESS_MODE_NONE
	for _, mode := range modes {
		if mode > max {
			max = mode
		}
	}
	return max
}

// FIXME: do corss-comparison so reduce deletions and additions to the minimum?
func (repo *Repository) refreshAccesses(e Engine, accessMap map[int64]AccessMode) (err error) {
	minMode := ACCESS_MODE_READ
	if !repo.IsPrivate {
		minMode = ACCESS_MODE_WRITE
	}

	newAccesses := make([]Access, 0, len(accessMap))
	for userID, mode := range accessMap {
		if mode < minMode {
			continue
		}
		newAccesses = append(newAccesses, Access{
			UserID: userID,
			RepoID: repo.ID,
			Mode:   mode,
		})
	}

	// Delete old accesses and insert new ones for repository.
	if _, err = e.Delete(&Access{RepoID: repo.ID}); err != nil {
		return fmt.Errorf("delete old accesses: %v", err)
	} else if _, err = e.Insert(newAccesses); err != nil {
		return fmt.Errorf("insert new accesses: %v", err)
	}
	return nil
}

// FIXME: should be able to have read-only access.
// Give all collaborators write access.
func (repo *Repository) refreshCollaboratorAccesses(e Engine, accessMap map[int64]AccessMode) error {
	collaborators, err := repo.getCollaborators(e)
	if err != nil {
		return fmt.Errorf("getCollaborators: %v", err)
	}
	for _, c := range collaborators {
		accessMap[c.Id] = ACCESS_MODE_WRITE
	}

	// Adds team members access.
	if repo.Owner.IsOrganization() {
		if err = repo.Owner.GetTeams(); err != nil {
			return fmt.Errorf("GetTeams: %v", err)
		}
		for _, t := range repo.Owner.Teams {
			if err = t.GetMembers(); err != nil {
				return fmt.Errorf("GetMembers: %v", err)
			}
			for _, m := range t.Members {
				if t.IsOwnerTeam() {
					accessMap[m.Id] = ACCESS_MODE_OWNER
				} else {
					accessMap[m.Id] = maxAccessMode(accessMap[m.Id], t.Authorize)
				}
			}
		}
	}
	return nil
}

// recalculateTeamAccesses recalculates new accesses for teams of an organization
// except the team whose ID is given. It is used to assign a team ID when
// remove repository from that team.
func (repo *Repository) recalculateTeamAccesses(e Engine, ignTeamID int64) (err error) {
	accessMap := make(map[int64]AccessMode, 20)

	if err = repo.getOwner(e); err != nil {
		return err
	}
	if err = repo.refreshCollaboratorAccesses(e, accessMap); err != nil {
		return fmt.Errorf("refreshCollaboratorAccesses: %v", err)
	}
	if repo.Owner.IsOrganization() {
		if err = repo.Owner.getTeams(e); err != nil {
			return err
		}

		for _, t := range repo.Owner.Teams {
			if t.ID == ignTeamID {
				continue
			}

			// Owner team gets owner access, and skip for teams that do not
			// have relations with repository.
			if t.IsOwnerTeam() {
				t.Authorize = ACCESS_MODE_OWNER
			} else if !t.hasRepository(e, repo.ID) {
				continue
			}

			if err = t.getMembers(e); err != nil {
				return fmt.Errorf("getMembers '%d': %v", t.ID, err)
			}
			for _, m := range t.Members {
				accessMap[m.Id] = maxAccessMode(accessMap[m.Id], t.Authorize)
			}
		}
	}

	return repo.refreshAccesses(e, accessMap)
}

func (repo *Repository) recalculateAccesses(e Engine) error {
	accessMap := make(map[int64]AccessMode, 20)
	if err := repo.refreshCollaboratorAccesses(e, accessMap); err != nil {
		return fmt.Errorf("refreshCollaboratorAccesses: %v", err)
	}
	return repo.refreshAccesses(e, accessMap)
}

// RecalculateAccesses recalculates all accesses for repository.
func (r *Repository) RecalculateAccesses() error {
	return r.recalculateAccesses(x)
}
